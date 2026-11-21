package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/ccusage"
	"github.com/tmc/cc/internal/antigravityread"
)

var (
	antigravityUserInformationRE = regexp.MustCompile(`(?is)<user_information>.*?</user_information>`)
	antigravityXMLTagRE          = regexp.MustCompile(`(?i)</?[a-z_][^>]*>`)
)

// Antigravity collects sessions from Google Antigravity.
type Antigravity struct {
	// Root overrides the default ~/.gemini/antigravity directory.
	Root string
}

// Name returns the agent slug "antigravity".
func (c *Antigravity) Name() string { return "antigravity" }

// Detect reports whether Antigravity session data is present on the system.
func (c *Antigravity) Detect(ctx context.Context) (*cass.DetectionResult, error) {
	roots, err := c.roots()
	if err != nil {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	var paths []string
	for _, root := range roots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			paths = append(paths, root)
		}
	}
	return &cass.DetectionResult{
		Agent: c.Name(),
		Found: len(paths) > 0,
		Paths: paths,
	}, nil
}

// Scan walks Antigravity brain artifact directories and sends decoded sessions
// to out. It closes out when scanning completes.
func (c *Antigravity) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		var err error
		paths, err = c.roots()
		if err != nil {
			return err
		}
	}

	for _, root := range paths {
		if err := c.scanPath(ctx, root, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Antigravity) roots() ([]string, error) {
	if c.Root != "" {
		return []string{c.Root}, nil
	}
	root, err := ccpaths.AntigravityHome()
	if err != nil {
		return nil, err
	}
	roots := []string{root}
	if os.Getenv("ANTIGRAVITY_HOME") == "" {
		roots = append(roots, filepath.Join(filepath.Dir(root), "antigravity-cli"))
	}
	return roots, nil
}

func (c *Antigravity) scanPath(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		for dir := filepath.Dir(root); dir != "." && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if antigravityread.IsSessionPath(dir) {
				return c.emitSession(ctx, dir, config, out)
			}
		}
		return nil
	}

	if antigravityread.IsSessionPath(root) {
		return c.emitSession(ctx, root, config, out)
	}

	brain := root
	if filepath.Base(root) != "brain" {
		brain = filepath.Join(root, "brain")
	}
	if info, err := os.Stat(brain); err != nil || !info.IsDir() {
		return nil
	}
	return c.scanBrainRoot(ctx, brain, config, out)
}

func (c *Antigravity) scanBrainRoot(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read antigravity brain dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if !antigravityread.IsSessionPath(dir) {
			continue
		}
		if err := c.emitSession(ctx, dir, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Antigravity) emitSession(ctx context.Context, dir string, config cass.ScanConfig, out chan<- cass.Session) error {
	session, err := c.parseBrainSession(dir)
	if err != nil {
		return nil
	}
	if !config.Since.IsZero() && session.EndedAt.Before(config.Since) {
		return nil
	}
	if config.Project != "" && !matchProject(session, config.Project) {
		return nil
	}

	select {
	case out <- session:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (c *Antigravity) parseBrainSession(dir string) (cass.Session, error) {
	entries, err := cc.ReadFile(context.Background(), dir)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", dir)
	}

	sum := cc.Summarize(dir, entries)

	var messages []cass.Message
	var turns int
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := strings.TrimSpace(e.Message.TextContent())
		if text == "" {
			continue
		}
		// Calculate turns like claudecode/pi collectors
		if e.Message.Role == "user" && !e.Message.IsToolResultOnly() {
			turns++
		}
		messages = append(messages, cass.Message{
			ID:        e.UUID,
			Role:      e.Message.Role,
			Content:   text,
			CreatedAt: e.Timestamp,
		})
	}

	stats := ExtractStats(entries)
	// Override turns with our computed count to keep consistency
	stats.Turns = turns

	links := ExtractLinks(entries)
	skills := ExtractSkills(entries, c.Name())

	meta := map[string]any{
		"brain_id": filepath.Base(dir),
	}
	if sum.Version != "" {
		meta["version"] = sum.Version
	}
	if sum.Model != "" {
		meta["model"] = sum.Model
	}
	if len(links) > 0 {
		meta["session_links"] = links
	}

	meta["artifact_count"] = len(messages)

	// Preserve legacy artifacts list for compatibility with existing DB queries
	var artifacts []map[string]string
	for _, e := range entries {
		if strings.Contains(e.UUID, ":") {
			parts := strings.Split(e.UUID, ":")
			fileName := parts[len(parts)-1]
			artifacts = append(artifacts, map[string]string{
				"file": fileName,
			})
		}
	}
	meta["artifacts"] = artifacts

	id := sum.SessionID
	if id == "" {
		id = filepath.Base(dir)
	}
	if !strings.HasPrefix(id, "ag-") {
		id = "ag-" + id
	}

	workspace := sum.CWD
	if workspace == "" {
		for _, e := range entries {
			if e.CWD != "" {
				workspace = e.CWD
				break
			}
		}
	}

	subagents := extractAntigravitySubagentRuns(context.Background(), dir, entries, cass.Session{ID: id, Workspace: workspace})
	if len(subagents) > 0 {
		stats.SubagentSpawns = len(subagents)
	}

	durationSecs := stats.DurationSecs
	if durationSecs == 0 && !sum.FirstTime.IsZero() && !sum.LastTime.IsZero() {
		durationSecs = int(sum.LastTime.Sub(sum.FirstTime).Seconds())
	}
	stats.DurationSecs = durationSecs

	return cass.Session{
		ID:         id,
		Agent:      c.Name(),
		Title:      antigravityTitle(entries, dir),
		Workspace:  workspace,
		SourcePath: dir,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Subagents:  subagents,
		Skills:     skills,
		Stats:      stats,
		Usage:      cass.UsageEvents(ccusage.FromEntries(c.Name(), entries)),
		Metadata:   meta,
	}, nil
}

func extractAntigravitySubagentRuns(ctx context.Context, sessionDir string, parentEntries []cc.Entry, parentSession cass.Session) []cass.SubagentRun {
	results := make(map[string]string)
	for _, e := range parentEntries {
		if e.Message == nil {
			continue
		}
		for _, result := range e.Message.ToolResults() {
			results[result.ToolUseID] = result.Content
		}
	}

	var runs []cass.SubagentRun
	for _, e := range parentEntries {
		if e.Message == nil {
			continue
		}
		for _, tool := range e.Message.ToolUses() {
			if tool.Name != "Task" {
				continue
			}
			specs := parseAntigravitySubagentSpecs(tool.Input)
			children := parseAntigravitySubagentResults(results[tool.ID])
			for i, child := range children {
				var spec antigravitySubagentSpec
				if i < len(specs) {
					spec = specs[i]
				}
				sourcePath := antigravityFilePath(child.LogAbsoluteURI)
				entries, err := cc.ReadFile(ctx, sourcePath)
				if err != nil {
					continue
				}
				workspace := parentSession.Workspace
				if len(child.WorkspaceURIs) > 0 {
					workspace = antigravityFilePath(child.WorkspaceURIs[0])
				}
				run := cass.SubagentRun{
					AgentID:         child.ConversationID,
					ParentSessionID: parentSession.ID,
					Workspace:       workspace,
					GitCommonDir:    parentSession.GitCommonDir,
					AgentType:       antigravityFirstNonEmpty(spec.TypeName, spec.Role),
					Description:     spec.Prompt,
					Model:           spec.Model,
					ToolUseID:       tool.ID,
					Status:          "unknown",
					SourcePath:      sourcePath,
					EntryCount:      len(entries),
				}
				populateRunFromEntries(&run, entries)
				runs = append(runs, run)
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	return runs
}

type antigravitySubagentSpec struct {
	Model    string `json:"Model"`
	Prompt   string `json:"Prompt"`
	Role     string `json:"Role"`
	TypeName string `json:"TypeName"`
}

type antigravitySubagentResult struct {
	ConversationID string   `json:"conversationId"`
	LogAbsoluteURI string   `json:"logAbsoluteUri"`
	WorkspaceURIs  []string `json:"workspaceUris"`
}

func parseAntigravitySubagentSpecs(input json.RawMessage) []antigravitySubagentSpec {
	var args struct {
		Subagents json.RawMessage `json:"Subagents"`
	}
	if json.Unmarshal(input, &args) != nil || len(args.Subagents) == 0 {
		return nil
	}
	var specs []antigravitySubagentSpec
	if json.Unmarshal(args.Subagents, &specs) != nil {
		var encoded string
		if json.Unmarshal(args.Subagents, &encoded) != nil || json.Unmarshal([]byte(encoded), &specs) != nil {
			return nil
		}
	}
	for i := range specs {
		specs[i].Model = cleanAntigravityValue(specs[i].Model)
		specs[i].Prompt = cleanAntigravityValue(specs[i].Prompt)
		specs[i].Role = cleanAntigravityValue(specs[i].Role)
		specs[i].TypeName = cleanAntigravityValue(specs[i].TypeName)
	}
	return specs
}

func parseAntigravitySubagentResults(content string) []antigravitySubagentResult {
	var results []antigravitySubagentResult
	for offset := 0; offset < len(content); {
		i := strings.IndexByte(content[offset:], '{')
		if i < 0 {
			break
		}
		offset += i
		dec := json.NewDecoder(strings.NewReader(content[offset:]))
		var result antigravitySubagentResult
		if err := dec.Decode(&result); err == nil && result.ConversationID != "" {
			results = append(results, result)
			offset += int(dec.InputOffset())
			continue
		}
		offset++
	}
	return results
}

func cleanAntigravityValue(s string) string {
	s = strings.TrimSpace(s)
	for range 2 {
		var unquoted string
		if json.Unmarshal([]byte(s), &unquoted) != nil {
			break
		}
		s = strings.TrimSpace(unquoted)
	}
	return s
}

func antigravityFilePath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.Clean(u.Path)
}

func antigravityFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func antigravityTitle(entries []cc.Entry, dir string) string {
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "user" {
			continue
		}
		text := e.Message.TextContent()
		text = antigravityUserInformationRE.ReplaceAllString(text, "")
		text = antigravityXMLTagRE.ReplaceAllString(text, "")
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimLeft(line, "#")
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "- [ ]")
			line = strings.TrimPrefix(line, "- [x]")
			line = strings.TrimPrefix(line, "- [X]")
			line = strings.TrimSpace(line)
			if line != "" {
				if len(line) > 80 {
					return line[:80] + "..."
				}
				return line
			}
		}
	}
	return filepath.Base(dir)
}
