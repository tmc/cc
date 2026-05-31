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

// Antigravity collects sessions from Google Antigravity.
type Antigravity struct {
	// Root overrides the default ~/.gemini/antigravity directory.
	Root string
}

// Name returns the agent slug "antigravity".
func (c *Antigravity) Name() string { return "antigravity" }

// Detect reports whether Antigravity session data is present on the system.
func (c *Antigravity) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

// Scan walks Antigravity brain artifact directories and sends decoded sessions
// to out. It closes out when scanning completes.
func (c *Antigravity) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
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
		if err := c.scanPath(ctx, root, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Antigravity) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity"), nil
}

func (c *Antigravity) scanPath(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return nil
	}

	if isAntigravityBrainSessionDir(root) {
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
		if !isAntigravityBrainSessionDir(dir) {
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

type antigravityArtifact struct {
	name string
	path string
}

type antigravityArtifactMeta struct {
	ArtifactType string `json:"artifactType"`
	Summary      string `json:"summary"`
	UpdatedAt    string `json:"updatedAt"`
	Version      string `json:"version"`
}

type antigravityWorkspaceFile struct {
	Folders []struct {
		Path string `json:"path"`
	} `json:"folders"`
}

func (c *Antigravity) parseBrainSession(dir string) (cass.Session, error) {
	files, err := antigravityArtifactFiles(dir)
	if err != nil {
		return cass.Session{}, err
	}
	if len(files) == 0 {
		return cass.Session{}, fmt.Errorf("empty antigravity brain session: %s", dir)
	}

	id := filepath.Base(dir)
	session := cass.Session{
		ID:         "ag-" + id,
		Agent:      c.Name(),
		SourcePath: dir,
		Workspace:  antigravityWorkspace(dir),
		Metadata: map[string]any{
			"brain_id": id,
		},
	}

	var artifacts []map[string]string
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}

		meta := readAntigravityArtifactMeta(file.path + ".metadata.json")
		t := antigravityArtifactTime(meta, file.path)
		if !t.IsZero() {
			if session.StartedAt.IsZero() || t.Before(session.StartedAt) {
				session.StartedAt = t
			}
			if session.EndedAt.IsZero() || t.After(session.EndedAt) {
				session.EndedAt = t
			}
		}

		role := "assistant"
		if file.name == "task.md" || file.name == "agent_prompt.md" {
			role = "user"
			session.Stats.Turns++
		}
		session.Messages = append(session.Messages, cass.Message{
			ID:        id + ":" + file.name,
			Role:      role,
			Content:   text,
			CreatedAt: t,
		})

		artifact := map[string]string{"file": file.name}
		if meta.ArtifactType != "" {
			artifact["type"] = meta.ArtifactType
		}
		if meta.Version != "" {
			artifact["version"] = meta.Version
		}
		if meta.Summary != "" {
			artifact["summary"] = meta.Summary
		}
		artifacts = append(artifacts, artifact)
	}
	if len(session.Messages) == 0 {
		return cass.Session{}, fmt.Errorf("empty antigravity messages: %s", dir)
	}
	if session.Title == "" {
		session.Title = antigravityTitle(session.Messages, artifacts, id)
	}
	if !session.StartedAt.IsZero() && !session.EndedAt.IsZero() {
		session.Stats.DurationSecs = int(session.EndedAt.Sub(session.StartedAt).Seconds())
	}
	session.Metadata["artifact_count"] = len(session.Messages)
	session.Metadata["artifacts"] = artifacts
	return session, nil
}

func isAntigravityBrainSessionDir(dir string) bool {
	files, err := antigravityArtifactFiles(dir)
	return err == nil && len(files) > 0
}

func antigravityArtifactFiles(dir string) ([]antigravityArtifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []antigravityArtifact
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".metadata.json") || strings.Contains(name, ".resolved") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".md" && ext != ".txt" {
			continue
		}
		files = append(files, antigravityArtifact{
			name: name,
			path: filepath.Join(dir, name),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		ri, rj := antigravityArtifactRank(files[i].name), antigravityArtifactRank(files[j].name)
		if ri != rj {
			return ri < rj
		}
		return files[i].name < files[j].name
	})
	return files, nil
}

func antigravityArtifactRank(name string) int {
	switch name {
	case "task.md":
		return 0
	case "agent_prompt.md":
		return 1
	case "implementation_plan.md":
		return 2
	case "walkthrough.md":
		return 3
	}
	if strings.HasSuffix(name, ".md") {
		return 4
	}
	return 5
}

func readAntigravityArtifactMeta(path string) antigravityArtifactMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return antigravityArtifactMeta{}
	}
	var meta antigravityArtifactMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return antigravityArtifactMeta{}
	}
	return meta
}

func antigravityArtifactTime(meta antigravityArtifactMeta, path string) time.Time {
	if meta.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, meta.UpdatedAt); err == nil {
			return t
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func antigravityTitle(messages []cass.Message, artifacts []map[string]string, id string) string {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		if title := firstAntigravityTitleLine(m.Content); title != "" {
			return truncateTitle(title)
		}
	}
	for _, artifact := range artifacts {
		if summary := strings.TrimSpace(artifact["summary"]); summary != "" {
			return truncateTitle(summary)
		}
	}
	return id
}

func firstAntigravityTitleLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- [ ]")
		line = strings.TrimPrefix(line, "- [x]")
		line = strings.TrimPrefix(line, "- [X]")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func antigravityWorkspace(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".code-workspace") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ws antigravityWorkspaceFile
		if err := json.Unmarshal(data, &ws); err != nil {
			continue
		}
		for _, folder := range ws.Folders {
			if folder.Path == "" {
				continue
			}
			return cleanAntigravityWorkspacePath(dir, folder.Path)
		}
	}
	return ""
}

func cleanAntigravityWorkspacePath(base, path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}
