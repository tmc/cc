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

func (c *ClaudeCode) Name() string { return "claude-code" }

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
	entries, err := cc.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
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

	// Extract agent teams context from entries.
	var teamName, agentName string
	for _, e := range entries {
		if e.TeamName != "" && teamName == "" {
			teamName = e.TeamName
		}
		if e.AgentName != "" && agentName == "" {
			agentName = e.AgentName
		}
		if teamName != "" && agentName != "" {
			break
		}
	}

	return cass.Session{
		ID:         id,
		Agent:      "claude-code",
		Title:      titleFromSummary(sum),
		Workspace:  workspace,
		SourcePath: path,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Stats:      stats,
		Metadata:   meta,
		TeamName:   teamName,
		AgentName:  agentName,
	}, nil
}

func (c *ClaudeCode) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
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
	if len(remaining) == 0 {
		_, err := os.Stat(current)
		return current, err == nil
	}

	seg := remaining[0]
	rest := remaining[1:]

	// Try "/" (path separator), "." (dot in name), "-" (literal dash).
	// For each, recurse and return the first result that resolves to
	// a real path.
	for _, sep := range []string{"/", ".", "-"} {
		candidate := current + sep + seg
		if result, ok := decodeSegments(candidate, rest); ok {
			return result, true
		}
	}

	return "", false
}
