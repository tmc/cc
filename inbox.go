package cc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// InboxMessage is a single message in an agent's inbox file.
type InboxMessage struct {
	From      string `json:"from"`
	Text      string `json:"text"`
	Summary   string `json:"summary,omitempty"`
	Timestamp string `json:"timestamp"`
	Color     string `json:"color,omitempty"`
	Read      bool   `json:"read"`
}

// StructuredMessage is parsed from InboxMessage.Text as JSON.
// The Type field discriminates the message kind.
type StructuredMessage struct {
	Type string `json:"type"`

	// task_assignment
	TaskID      string `json:"taskId,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Description string `json:"description,omitempty"`
	AssignedBy  string `json:"assignedBy,omitempty"`

	// shutdown_request / shutdown_approved
	RequestID   string `json:"requestId,omitempty"`
	From        string `json:"from,omitempty"`
	Reason      string `json:"reason,omitempty"`
	PaneID      string `json:"paneId,omitempty"`
	BackendType string `json:"backendType,omitempty"`

	// idle_notification
	IdleReason string `json:"idleReason,omitempty"`

	// plan_approval_request / plan_approval_response
	PlanContent string `json:"planContent,omitempty"`
	Approved    *bool  `json:"approved,omitempty"`
	Feedback    string `json:"feedback,omitempty"`

	// permission_request / permission_response
	ToolName              string   `json:"toolName,omitempty"`
	ToolUseID             string   `json:"toolUseId,omitempty"`
	Input                 any      `json:"input,omitempty"`
	PermissionSuggestions []string `json:"permissionSuggestions,omitempty"`

	// plain_text
	Text string `json:"text,omitempty"`

	// common
	Timestamp string `json:"timestamp,omitempty"`
}

// InboxDir returns the path to a team's inboxes directory.
func InboxDir(teamName string) (string, error) {
	dir, err := TeamDir(teamName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "inboxes"), nil
}

// InboxPath returns the path to an agent's inbox file.
func InboxPath(teamName, agentName string) (string, error) {
	dir, err := InboxDir(teamName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentName+".json"), nil
}

// ReadInbox reads all messages from an agent's inbox.
func ReadInbox(teamName, agentName string) ([]InboxMessage, error) {
	path, err := InboxPath(teamName, agentName)
	if err != nil {
		return nil, err
	}
	return readInboxFile(path)
}

// ReadUnread reads unread messages and marks them as read atomically.
func ReadUnread(teamName, agentName string) ([]InboxMessage, error) {
	path, err := InboxPath(teamName, agentName)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open inbox %q: %w", agentName, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return nil, err
	}
	defer unlockFile(f)

	var msgs []InboxMessage
	if err := json.NewDecoder(f).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("parse inbox %q: %w", agentName, err)
	}

	var unread []InboxMessage
	for i := range msgs {
		if !msgs[i].Read {
			unread = append(unread, msgs[i])
			msgs[i].Read = true
		}
	}
	if len(unread) == 0 {
		return nil, nil
	}

	// Rewrite file with updated read flags.
	if err := f.Truncate(0); err != nil {
		return unread, fmt.Errorf("truncate inbox: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return unread, fmt.Errorf("seek inbox: %w", err)
	}
	data, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		return unread, fmt.Errorf("marshal inbox: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return unread, fmt.Errorf("write inbox: %w", err)
	}
	return unread, nil
}

// AppendInbox appends a message to an agent's inbox with file locking.
func AppendInbox(teamName, agentName string, msg InboxMessage) error {
	path, err := InboxPath(teamName, agentName)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create inbox dir: %w", err)
	}

	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open inbox %q: %w", agentName, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)

	var msgs []InboxMessage
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat inbox: %w", err)
	}
	if info.Size() > 0 {
		if err := json.NewDecoder(f).Decode(&msgs); err != nil {
			return fmt.Errorf("parse inbox %q: %w", agentName, err)
		}
	}

	msgs = append(msgs, msg)

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate inbox: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek inbox: %w", err)
	}
	data, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal inbox: %w", err)
	}
	_, err = f.Write(data)
	return err
}

// ParseMessage attempts to parse an InboxMessage's text as a StructuredMessage.
// Returns nil if the text is not valid JSON with a type field.
func ParseMessage(msg InboxMessage) *StructuredMessage {
	var sm StructuredMessage
	if err := json.Unmarshal([]byte(msg.Text), &sm); err != nil {
		return nil
	}
	if sm.Type == "" {
		return nil
	}
	return &sm
}

func readInboxFile(path string) ([]InboxMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read inbox: %w", err)
	}
	var msgs []InboxMessage
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, fmt.Errorf("parse inbox: %w", err)
	}
	return msgs, nil
}

func lockFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

func lockFileShared(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return fmt.Errorf("rlock file: %w", err)
	}
	return nil
}

func unlockFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
