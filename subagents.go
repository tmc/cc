package cc

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Subagent links a parent's transcript to one direct child. Claude children
// share the parent's SessionID and use the filename's AgentID; Codex children
// have their own SessionID, used as AgentID. ToolUseID is empty when the
// harness provides no authoritative tool-call join. Entries are child entries
// tagged with AgentID and IsSidechain. Notification and Meta are Claude data.
type Subagent struct {
	ParentSessionID string
	AgentID         string
	SourcePath      string
	ToolUseID       string
	Entries         []Entry
	Notification    *TaskNotification
	Meta            SubagentMeta
	IsCompaction    bool
}

// Subagents reads direct child transcripts and their parent linkage. Claude
// children are found in <path-without-.jsonl>/subagents/agent-*.jsonl; Codex
// children are found by ParentThreadID in the containing sessions tree.
// Missing Claude subagent directories return no children. Read and metadata
// errors are returned instead of silently omitting children. A canceled context
// interrupts discovery. It does not resolve git metadata or use the cass index.
func Subagents(ctx context.Context, path string) ([]Subagent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent, err := ReadFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read parent session: %w", err)
	}
	parentID := ""
	codex := false
	for _, e := range parent {
		if parentID == "" {
			parentID = e.SessionID
		}
		if e.Subtype == "session_meta" && (e.Source == "" || e.Source == "codex") {
			codex = true
			parentID = e.SessionID
		}
	}
	if codex {
		return codexSubagents(ctx, path, parentID)
	}
	if !strings.HasSuffix(path, ".jsonl") {
		return nil, nil
	}
	dir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subagents: %w", err)
	}
	notes := make(map[string]TaskNotification)
	for _, e := range parent {
		text := e.Content
		if e.Message != nil {
			text = e.Message.TextContent()
		}
		if note, ok := ParseTaskNotification(text); ok {
			notes[note.TaskID] = note
		}
	}
	var result []Subagent
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := file.Name()
		if file.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		if id == "" {
			continue
		}
		source := filepath.Join(dir, name)
		entries, err := ReadFile(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("read subagent %q: %w", source, err)
		}
		meta, err := ReadSubagentMeta(strings.TrimSuffix(source, ".jsonl") + ".meta.json")
		if err != nil {
			return nil, err
		}
		child := Subagent{ParentSessionID: parentID, AgentID: id, SourcePath: source, Entries: entries, Meta: meta, IsCompaction: strings.HasPrefix(id, "acompact")}
		if note, ok := notes[id]; ok {
			child.Notification = &note
			child.ToolUseID = note.ToolUseID
		}
		tagSubagent(&child)
		result = append(result, child)
	}
	return result, nil
}

func tagSubagent(child *Subagent) {
	for i := range child.Entries {
		child.Entries[i].AgentID = child.AgentID
		child.Entries[i].IsSidechain = true
	}
}

func codexSubagents(ctx context.Context, path, parentID string) ([]Subagent, error) {
	if parentID == "" {
		return nil, fmt.Errorf("codex parent session has no id")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(abs)
	for filepath.Base(root) != "sessions" {
		next := filepath.Dir(root)
		if next == root {
			return nil, fmt.Errorf("codex session %q is outside a sessions tree", path)
		}
		root = next
	}
	var result []Subagent
	err = filepath.WalkDir(root, func(candidate string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || candidate == abs || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		entries, err := ReadFile(ctx, candidate)
		if err != nil {
			return fmt.Errorf("read candidate %q: %w", candidate, err)
		}
		var meta Entry
		for _, e := range entries {
			if e.Subtype == "session_meta" {
				meta = e
			}
		}
		if meta.ParentThreadID != parentID {
			return nil
		}
		child := Subagent{ParentSessionID: parentID, AgentID: meta.SessionID, SourcePath: candidate, Entries: entries}
		tagSubagent(&child)
		result = append(result, child)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover codex subagents: %w", err)
	}
	return result, nil
}
