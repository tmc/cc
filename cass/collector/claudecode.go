package collector

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// ClaudeCode collects sessions from Claude Code's JSONL session files.
type ClaudeCode struct {
	// Root overrides the default ~/.claude/projects directory.
	Root string
}

// Name returns the agent slug "claude-code".
func (c *ClaudeCode) Name() string { return "claude-code" }

// Detect reports whether Claude Code session data is present on the system.
func (c *ClaudeCode) Detect(ctx context.Context) (*cass.DetectionResult, error) {
	root, err := c.root()
	if err != nil {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	return &cass.DetectionResult{
		Agent: c.Name(),
		Found: true,
		Paths: []string{root},
	}, nil
}

// Scan walks Claude Code session paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *ClaudeCode) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
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
		if err := c.scanDir(ctx, root, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *ClaudeCode) scanDir(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Skip subagents/ directories during the Walk: subagent entries are
		// merged into their parent session by parseSession instead of being
		// emitted as separate sessions (they share the parent's sessionId).
		if info.IsDir() && info.Name() == "subagents" {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}
		if config.Project != "" {
			rel, _ := filepath.Rel(root, path)
			if !strings.Contains(strings.ToLower(rel), strings.ToLower(config.Project)) {
				return nil
			}
		}

		session, err := c.parseSession(path)
		if err != nil {
			return nil // skip unparseable files
		}

		select {
		case out <- session:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
}

func (c *ClaudeCode) parseSession(path string) (cass.Session, error) {
	parentEntries, err := cc.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(parentEntries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
	}

	// Merge subagent entries into a separate slice for content/FTS purposes.
	// Subagent files live at:
	//   <parent-dir>/<parent-uuid>/subagents/agent-<agentId>.jsonl
	// They share the parent's sessionId, so their tokens accumulate correctly.
	// Exclude acompact-* files (compaction subagents that duplicate history).
	//
	// We keep parentEntries separate so queue-operation pairing in
	// extractSubagentRuns sees only the parent's events.
	entries := parentEntries
	subagentDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if infos, err := os.ReadDir(subagentDir); err == nil {
		for _, fi := range infos {
			name := fi.Name()
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			if strings.HasPrefix(name, "agent-acompact") {
				continue
			}
			sub, err := cc.ReadFile(filepath.Join(subagentDir, name))
			if err == nil {
				entries = append(entries, sub...)
			}
		}
	}

	sum := cc.Summarize(path, entries)

	// Build normalized messages.
	var messages []cass.Message
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := e.Message.TextContent()
		if text == "" {
			continue
		}
		msg := cass.Message{
			ID:        e.UUID,
			Role:      e.Message.Role,
			Content:   text,
			CreatedAt: e.Timestamp,
		}
		messages = append(messages, msg)
	}

	// Derive workspace from the encoded project path.
	workspace := workspaceFromPath(path)

	// Generate a stable ID from source path.
	id := sessionID(path)

	// Extract inter-session communication links and stats.
	links := ExtractLinks(entries)
	stats := ExtractStats(entries)
	skills := ExtractSkills(entries, "claude-code")
	goals := ExtractClaudeGoals(entries)
	workflows := ExtractWorkflows(path, parentEntries)
	for _, w := range workflows {
		stats.WorkflowAgentRuns += w.AgentCount
	}

	// Extract team links (native Claude Code agent teams).
	teamLinks := ExtractTeamLinks(entries)
	links = append(links, teamLinks...)

	// Build metadata.
	meta := map[string]any{}
	if sum.GitBranch != "" {
		meta["git_branch"] = sum.GitBranch
	}
	if sum.Model != "" {
		meta["model"] = sum.Model
	}
	if sum.Version != "" {
		meta["version"] = sum.Version
	}
	if len(links) > 0 {
		meta["session_links"] = links
	}

	// Try to extract the iTerm2 session ID from tool results.
	itermSID := extractItermSessionID(entries)
	if itermSID != "" {
		meta["iterm_session"] = itermSID
	}

	// Classify team role from JSONL data.
	teamName, agentName, isTeamLead := ClassifyTeamRole(entries)

	session := cass.Session{
		ID:           id,
		Agent:        "claude-code",
		Title:        titleFromSummary(sum),
		Workspace:    workspace,
		GitCommonDir: sum.GitCommonDir,
		Branch:       sum.Branch,
		SourcePath:   path,
		StartedAt:    sum.FirstTime,
		EndedAt:      sum.LastTime,
		Messages:     messages,
		Skills:       skills,
		Goals:        goals,
		Stats:        stats,
		Metadata:     meta,
		Workflows:    workflows,
		TeamName:     teamName,
		AgentName:    agentName,
		IsTeamLead:   isTeamLead,
	}
	session.Subagents = extractSubagentRuns(path, parentEntries, session)
	return session, nil
}

func (c *ClaudeCode) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	ch, err := cc.ClaudeHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(ch, "projects"), nil
}

func sessionID(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", h[:16])
}

func titleFromSummary(s cc.SessionSummary) string {
	if s.CustomTitle != "" {
		return s.CustomTitle
	}
	if s.FirstPrompt != "" {
		t := s.FirstPrompt
		if len(t) > 80 {
			t = t[:80] + "..."
		}
		return t
	}
	return filepath.Base(s.File)
}

// workspaceFromPath extracts the original workspace path from the encoded
// Claude Code project directory name (e.g. "-Volumes-tmc-go-src-..." -> "/Volumes/tmc/go/src/...").
//
// Claude Code encodes paths by replacing "/" with "-". This is ambiguous when
// directory names contain literal dashes (e.g. "chrome-to-har"). We resolve
// the ambiguity by checking the filesystem: at each dash, we try treating it
// as a path separator first (most dashes are separators); if that directory
// exists we commit to it, otherwise we keep the dash as literal and continue.
func workspaceFromPath(sessionPath string) string {
	// Session files live under ~/.claude/projects/<encoded-path>/...
	dir := filepath.Dir(sessionPath)
	for {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == "projects" {
			break
		}
		if parent == dir {
			return ""
		}
		dir = parent
	}
	encoded := filepath.Base(dir)
	if encoded == "" || encoded == "." {
		return ""
	}
	return decodePath(encoded)
}

// decodePath reconstructs the original filesystem path from an encoded
// Claude Code project directory name. Claude Code encodes paths by
// replacing both "/" and "." with "-", so each dash is ambiguous.
// We resolve by checking the filesystem, using backtracking to handle
// multi-dash directory names like "chrome-to-har" and dotted names
// like "github.com".
func decodePath(encoded string) string {
	var prefix string
	if strings.HasPrefix(encoded, "-") {
		prefix = "/"
		encoded = encoded[1:]
	}

	segments := strings.Split(encoded, "-")
	if len(segments) == 0 {
		return prefix
	}

	if result, ok := decodeSegments(prefix+segments[0], segments[1:]); ok {
		return result
	}
	// Fallback: simple "/" replacement.
	return prefix + strings.Join(segments, "/")
}

// decodeSegments tries all possible decodings of dash-separated segments
// by checking the filesystem. Returns the decoded path and whether it
// (or a prefix of it) exists on disk.
func decodeSegments(current string, remaining []string) (string, bool) {
	return cc.DecodeSegments(current, remaining)
}
