package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
)

// OpenCode collects sessions from opencode's JSON storage.
//
// opencode stores session metadata under
// ~/.local/share/opencode/storage/session/<project>/<session>.json, messages
// under storage/message/<session>/*.json, and message parts under
// storage/part/<message>/*.json.
type OpenCode struct {
	// Root overrides the default ~/.local/share/opencode/storage directory.
	Root string
}

// Name returns the agent slug "opencode".
func (c *OpenCode) Name() string { return "opencode" }

// Detect reports whether opencode session data is present on the system.
func (c *OpenCode) Detect(ctx context.Context) (*cass.DetectionResult, error) {
	root, err := c.root()
	if err != nil {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	sessionRoot := filepath.Join(root, "session")
	info, err := os.Stat(sessionRoot)
	if err != nil || !info.IsDir() {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	return &cass.DetectionResult{
		Agent: c.Name(),
		Found: true,
		Paths: []string{root},
	}, nil
}

// Scan walks opencode storage paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *OpenCode) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		root, err := c.root()
		if err != nil {
			return err
		}
		paths = []string{root}
	}

	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if filepath.Base(filepath.Dir(filepath.Dir(root))) != "session" && !strings.HasPrefix(filepath.Base(root), "ses_") {
				continue
			}
			if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
				continue
			}
			sess, err := c.parseSession(root)
			if err != nil {
				continue
			}
			if config.Project != "" && !matchProject(sess, config.Project) {
				continue
			}
			select {
			case out <- sess:
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		if err := c.scanRoot(ctx, root, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *OpenCode) scanRoot(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	sessionRoot := root
	if filepath.Base(root) != "session" {
		sessionRoot = filepath.Join(root, "session")
	}
	return filepath.Walk(sessionRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(path), "ses_") || !strings.HasSuffix(path, ".json") {
			return nil
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}

		sess, err := c.parseSession(path)
		if err != nil {
			return nil
		}
		if config.Project != "" && !matchProject(sess, config.Project) {
			return nil
		}
		select {
		case out <- sess:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
}

type openCodeSessionFile struct {
	ID        string          `json:"id"`
	Slug      string          `json:"slug"`
	Version   string          `json:"version"`
	ProjectID string          `json:"projectID"`
	Directory string          `json:"directory"`
	ParentID  string          `json:"parentID"`
	Title     string          `json:"title"`
	Time      openCodeTime    `json:"time"`
	Summary   openCodeSummary `json:"summary"`
}

type openCodeSummary struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

type openCodeMessageFile struct {
	ID         string             `json:"id"`
	SessionID  string             `json:"sessionID"`
	Role       string             `json:"role"`
	Time       openCodeTime       `json:"time"`
	ParentID   string             `json:"parentID"`
	ModelID    string             `json:"modelID"`
	ProviderID string             `json:"providerID"`
	Mode       string             `json:"mode"`
	Agent      string             `json:"agent"`
	Path       openCodePath       `json:"path"`
	Cost       float64            `json:"cost"`
	Tokens     openCodeTokens     `json:"tokens"`
	Finish     string             `json:"finish"`
	Summary    openCodeMsgSummary `json:"summary"`
	Model      openCodeModel      `json:"model"`
}

type openCodeMsgSummary struct {
	Title string `json:"title"`
}

type openCodeModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

type openCodePath struct {
	CWD  string `json:"cwd"`
	Root string `json:"root"`
}

type openCodeTime struct {
	Created   int64 `json:"created"`
	Updated   int64 `json:"updated"`
	Completed int64 `json:"completed"`
}

type openCodeTokens struct {
	Input     int                `json:"input"`
	Output    int                `json:"output"`
	Reasoning int                `json:"reasoning"`
	Cache     openCodeTokenCache `json:"cache"`
}

type openCodeTokenCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type openCodePartFile struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionID"`
	MessageID string         `json:"messageID"`
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Tool      string         `json:"tool"`
	State     map[string]any `json:"state"`
	Tokens    openCodeTokens `json:"tokens"`
	Time      openCodeTime   `json:"time"`
}

func (c *OpenCode) parseSession(path string) (cass.Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}
	var sf openCodeSessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return cass.Session{}, err
	}
	if sf.ID == "" {
		return cass.Session{}, fmt.Errorf("opencode session missing id: %s", path)
	}

	root := c.storageRootFromSessionPath(path)
	messages, stats, meta, err := readOpenCodeMessages(root, sf.ID)
	if err != nil {
		return cass.Session{}, err
	}
	if sf.Version != "" {
		meta["version"] = sf.Version
	}
	if sf.ProjectID != "" {
		meta["project_id"] = sf.ProjectID
	}
	if sf.Slug != "" {
		meta["slug"] = sf.Slug
	}
	if sf.ParentID != "" {
		meta["parent_id"] = sf.ParentID
		meta["session_links"] = []cass.SessionLink{{
			Kind:          "opencode",
			Action:        "parent",
			SourceSession: sf.ID,
			TargetSession: sf.ParentID,
		}}
	}
	if sf.Summary.Files != 0 {
		stats.FilesEdited = sf.Summary.Files
	}
	if sf.Summary.Additions != 0 {
		stats.LinesWritten = sf.Summary.Additions
	}

	started := unixMillis(sf.Time.Created)
	ended := unixMillis(sf.Time.Updated)
	if started.IsZero() && len(messages) > 0 {
		started = messages[0].CreatedAt
	}
	if ended.IsZero() && len(messages) > 0 {
		ended = messages[len(messages)-1].CreatedAt
	}
	if !started.IsZero() && !ended.IsZero() {
		stats.DurationSecs = int(ended.Sub(started).Seconds())
	}

	title := strings.TrimSpace(sf.Title)
	if title == "" {
		title = titleFromMessages(messages)
	}

	return cass.Session{
		ID:         sf.ID,
		Agent:      c.Name(),
		Title:      title,
		Workspace:  sf.Directory,
		SourcePath: path,
		StartedAt:  started,
		EndedAt:    ended,
		Messages:   messages,
		Stats:      stats,
		Metadata:   meta,
	}, nil
}

func readOpenCodeMessages(root, sessionID string) ([]cass.Message, cass.SessionStats, map[string]any, error) {
	messageDir := filepath.Join(root, "message", sessionID)
	files, err := os.ReadDir(messageDir)
	if err != nil {
		return nil, cass.SessionStats{ToolBreakdown: map[string]int{}}, map[string]any{}, nil
	}

	var records []openCodeMessageFile
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(messageDir, f.Name()))
		if err != nil {
			continue
		}
		var msg openCodeMessageFile
		if err := json.Unmarshal(data, &msg); err != nil || msg.ID == "" {
			continue
		}
		records = append(records, msg)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Time.Created < records[j].Time.Created
	})

	stats := cass.SessionStats{ToolBreakdown: map[string]int{}}
	meta := map[string]any{}
	var messages []cass.Message
	var model, provider, mode, agent string
	var cost float64
	var last time.Time

	for _, rec := range records {
		parts := readOpenCodeParts(root, rec.ID)
		content, toolNames := openCodeMessageContent(rec, parts)
		created := unixMillis(rec.Time.Created)
		if !created.IsZero() {
			last = created
		}
		if strings.TrimSpace(content) != "" {
			messages = append(messages, cass.Message{
				ID:        rec.ID,
				Role:      rec.Role,
				Content:   strings.TrimSpace(content),
				CreatedAt: created,
			})
		}
		if rec.Role == "user" {
			stats.Turns++
		}
		for _, name := range toolNames {
			stats.ToolCalls++
			stats.ToolBreakdown[name]++
			countOpenCodeTool(name, &stats)
		}
		stats.InputTokens += rec.Tokens.Input
		stats.OutputTokensSnapshot += rec.Tokens.Output
		stats.CacheReads += rec.Tokens.Cache.Read
		stats.CacheCreationInputTokens += rec.Tokens.Cache.Write
		cost += rec.Cost
		model, provider = openCodeModelProvider(rec, model, provider)
		if rec.Mode != "" {
			mode = rec.Mode
		}
		if rec.Agent != "" {
			agent = rec.Agent
		}
	}
	if model != "" {
		meta["model"] = model
	}
	if provider != "" {
		meta["provider"] = provider
	}
	if mode != "" {
		meta["mode"] = mode
	}
	if agent != "" {
		meta["opencode_agent"] = agent
	}
	if cost != 0 {
		meta["cost"] = cost
	}
	if !last.IsZero() && len(messages) > 0 && messages[len(messages)-1].CreatedAt.IsZero() {
		messages[len(messages)-1].CreatedAt = last
	}
	stats.Sparkline = openCodeSparkline(messages)
	return messages, stats, meta, nil
}

func readOpenCodeParts(root, messageID string) []openCodePartFile {
	partDir := filepath.Join(root, "part", messageID)
	files, err := os.ReadDir(partDir)
	if err != nil {
		return nil
	}
	var parts []openCodePartFile
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(partDir, f.Name()))
		if err != nil {
			continue
		}
		var part openCodePartFile
		if err := json.Unmarshal(data, &part); err != nil || part.ID == "" {
			continue
		}
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].ID < parts[j].ID
	})
	return parts
}

func openCodeMessageContent(msg openCodeMessageFile, parts []openCodePartFile) (string, []string) {
	var text strings.Builder
	var tools []string
	if msg.Summary.Title != "" {
		text.WriteString(msg.Summary.Title)
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(part.Text)
			}
		case "tool":
			if part.Tool != "" {
				tools = append(tools, part.Tool)
			}
		}
	}
	return text.String(), tools
}

func openCodeModelProvider(msg openCodeMessageFile, model, provider string) (string, string) {
	if msg.ModelID != "" {
		model = msg.ModelID
	}
	if msg.ProviderID != "" {
		provider = msg.ProviderID
	}
	if msg.Model.ModelID != "" {
		model = msg.Model.ModelID
	}
	if msg.Model.ProviderID != "" {
		provider = msg.Model.ProviderID
	}
	return model, provider
}

func countOpenCodeTool(name string, stats *cass.SessionStats) {
	switch strings.ToLower(name) {
	case "read":
		stats.FilesRead++
	case "write":
		stats.FilesWritten++
	case "edit":
		stats.FilesEdited++
	case "bash":
		// Command contents are in a generic JSON object; keep the tool count
		// authoritative and leave command-specific it2 stats to JSONL readers.
	case "task":
		stats.SubagentSpawns++
		stats.TeamMembersSpawned++
	case "todowrite", "todoread":
		stats.TaskUpdates++
		stats.WorkflowTaskOps++
	}
}

func titleFromMessages(messages []cass.Message) string {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		for _, line := range strings.Split(m.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(line) > 80 {
				line = line[:77] + "..."
			}
			return line
		}
	}
	return ""
}

func openCodeSparkline(messages []cass.Message) string {
	if len(messages) == 0 {
		return ""
	}
	var entries []timestamped
	for _, msg := range messages {
		entries = append(entries, timestamped{Timestamp: msg.CreatedAt})
	}
	return buildSparklineFromTimes(entries)
}

type timestamped struct {
	Timestamp time.Time
}

func buildSparklineFromTimes(entries []timestamped) string {
	if len(entries) == 0 {
		return ""
	}
	first := entries[0].Timestamp
	last := entries[len(entries)-1].Timestamp
	if first.IsZero() || last.IsZero() || !last.After(first) {
		return ""
	}
	const buckets = 8
	counts := make([]int, buckets)
	duration := last.Sub(first)
	for _, e := range entries {
		if e.Timestamp.IsZero() {
			continue
		}
		idx := int(e.Timestamp.Sub(first) * buckets / duration)
		if idx >= buckets {
			idx = buckets - 1
		}
		if idx < 0 {
			idx = 0
		}
		counts[idx]++
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	if max == 0 {
		return ""
	}
	var out strings.Builder
	for _, n := range counts {
		if n == 0 {
			out.WriteRune(blocks[0])
			continue
		}
		out.WriteRune(blocks[(n*(len(blocks)-1))/max])
	}
	return out.String()
}

func unixMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond)).UTC()
}

func (c *OpenCode) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "opencode", "storage"), nil
}

func (c *OpenCode) storageRootFromSessionPath(path string) string {
	dir := filepath.Dir(path)
	for filepath.Base(dir) != "session" {
		next := filepath.Dir(dir)
		if next == dir {
			root, _ := c.root()
			return root
		}
		dir = next
	}
	return filepath.Dir(dir)
}
