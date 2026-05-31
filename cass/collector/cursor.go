package collector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
	_ "modernc.org/sqlite"
)

// Cursor collects sessions from Cursor IDE workspace storage.
type Cursor struct {
	// Root overrides the default workspaceStorage directory.
	Root string
}

// Name returns the agent slug "cursor".
func (c *Cursor) Name() string { return "cursor" }

// Detect reports whether Cursor workspace data is present on the system.
func (c *Cursor) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

// Scan walks Cursor workspace storage and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *Cursor) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		root, err := c.root()
		if err != nil {
			return err
		}
		paths = []string{root}
	}

	for _, path := range paths {
		if err := c.scanPath(ctx, path, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cursor) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// Default Cursor path on macOS (similar paths exist for Linux/Windows)
	return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage"), nil
}

func (c *Cursor) scanPath(ctx context.Context, path string, config cass.ScanConfig, out chan<- cass.Session) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		if filepath.Base(path) == "state.vscdb" {
			return c.scanDB(ctx, path, config, out)
		}
		return nil
	}

	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			switch d.Name() {
			case "Cache", "CachedData", "GPUCache", "logs":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "state.vscdb" {
			return nil
		}
		info, err := d.Info()
		if err == nil && !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}
		return c.scanDB(ctx, p, config, out)
	})
}

func (c *Cursor) scanDB(ctx context.Context, path string, config cass.ScanConfig, out chan<- cass.Session) error {
	db, err := sql.Open("sqlite", cursorReadOnlyURI(path))
	if err != nil {
		return nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	info, err := os.Stat(path)
	var modTime time.Time
	if err == nil {
		modTime = info.ModTime()
	}
	workspace := cursorWorkspace(path)

	tables := cursorStorageTables(ctx, db)
	seen := map[string]bool{}
	for _, table := range tables {
		if err := c.scanStorageTable(ctx, db, path, workspace, modTime, table, config, seen, out); err != nil {
			return err
		}
	}
	return nil
}

type cursorStorageTable struct {
	name     string
	keyCol   string
	valueCol string
}

func cursorStorageTables(ctx context.Context, db *sql.DB) []cursorStorageTable {
	candidates := []cursorStorageTable{
		{name: "ItemTable", keyCol: "key", valueCol: "value"},
		{name: "cursorDiskKV", keyCol: "key", valueCol: "value"},
	}
	var tables []cursorStorageTable
	for _, table := range candidates {
		if sqliteTableHasColumns(ctx, db, table.name, table.keyCol, table.valueCol) {
			tables = append(tables, table)
		}
	}
	return tables
}

func sqliteTableHasColumns(ctx context.Context, db *sql.DB, table string, want ...string) bool {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdent(table)+")")
	if err != nil {
		return false
	}
	defer rows.Close()

	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false
		}
		have[name] = true
	}
	for _, col := range want {
		if !have[col] {
			return false
		}
	}
	return true
}

func (c *Cursor) scanStorageTable(ctx context.Context, db *sql.DB, dbPath, workspace string, modTime time.Time, table cursorStorageTable, config cass.ScanConfig, seen map[string]bool, out chan<- cass.Session) error {
	query := fmt.Sprintf("SELECT %s, %s FROM %s", quoteSQLiteIdent(table.keyCol), quoteSQLiteIdent(table.valueCol), quoteSQLiteIdent(table.name))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			continue
		}
		sessions := cursorSessionsFromValue(dbPath, workspace, modTime, table.name, key, raw)
		for _, session := range sessions {
			if seen[session.ID] {
				continue
			}
			seen[session.ID] = true
			if !config.Since.IsZero() && session.EndedAt.Before(config.Since) {
				continue
			}
			if config.Project != "" && !matchProject(session, config.Project) {
				continue
			}
			select {
			case out <- session:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return rows.Err()
}

type cursorCandidate struct {
	pointer  string
	parent   map[string]any
	messages []cass.Message
}

func cursorSessionsFromValue(dbPath, workspace string, modTime time.Time, table, key string, raw []byte) []cass.Session {
	value, ok := decodeCursorJSON(raw)
	if !ok {
		return nil
	}

	var candidates []cursorCandidate
	walkCursorJSON(value, "", nil, &candidates)
	if len(candidates) == 0 {
		return nil
	}

	var sessions []cass.Session
	for _, candidate := range candidates {
		session := cursorSessionFromCandidate(dbPath, workspace, modTime, table, key, candidate)
		if len(session.Messages) == 0 {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

func decodeCursorJSON(raw []byte) (any, bool) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return nil, false
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err == nil {
			data = bytes.TrimSpace([]byte(s))
		}
	}
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

func walkCursorJSON(value any, pointer string, parent map[string]any, out *[]cursorCandidate) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if arr, ok := child.([]any); ok && isCursorMessageArrayName(key) {
				if messages := cursorMessagesFromArray(arr); len(messages) > 0 {
					*out = append(*out, cursorCandidate{
						pointer:  pointer + "/" + key,
						parent:   v,
						messages: messages,
					})
				}
			}
			walkCursorJSON(child, pointer+"/"+key, v, out)
		}
	case []any:
		for i, child := range v {
			walkCursorJSON(child, fmt.Sprintf("%s/%d", pointer, i), parent, out)
		}
	}
}

func isCursorMessageArrayName(name string) bool {
	switch strings.ToLower(name) {
	case "messages", "conversation", "conversationhistory", "chatmessages", "bubbles", "turns":
		return true
	}
	return false
}

func cursorMessagesFromArray(values []any) []cass.Message {
	var messages []cass.Message
	for i, value := range values {
		obj, ok := value.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := cursorMessageFromObject(obj)
		if !ok {
			continue
		}
		if msg.ID == "" {
			msg.ID = fmt.Sprintf("m%d", i+1)
		}
		messages = append(messages, msg)
	}
	if len(messages) == 0 || !cursorMessagesHaveConversationRoles(messages) {
		return nil
	}
	return messages
}

func cursorMessagesHaveConversationRoles(messages []cass.Message) bool {
	for _, msg := range messages {
		switch msg.Role {
		case "user", "assistant":
			return true
		}
	}
	return false
}

func cursorMessageFromObject(obj map[string]any) (cass.Message, bool) {
	body := obj
	if nested, ok := mapValue(obj["message"]); ok {
		body = mergeCursorObjects(obj, nested)
	}

	role := cursorRole(body)
	content := cursorContent(body)
	if role == "" || content == "" {
		return cass.Message{}, false
	}
	return cass.Message{
		ID:        firstCursorString(body, "id", "messageId", "bubbleId", "uuid"),
		Role:      role,
		Content:   content,
		CreatedAt: cursorTime(body),
	}, true
}

func mergeCursorObjects(base, overlay map[string]any) map[string]any {
	merged := map[string]any{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overlay {
		merged[k] = v
	}
	return merged
}

func cursorRole(obj map[string]any) string {
	for _, key := range []string{"role", "sender", "author", "type", "speaker"} {
		s := strings.ToLower(strings.TrimSpace(cursorString(obj[key])))
		switch s {
		case "user", "human", "client", "me":
			return "user"
		case "assistant", "ai", "bot", "agent", "cursor":
			return "assistant"
		case "system":
			return "system"
		}
	}
	return ""
}

func cursorContent(obj map[string]any) string {
	for _, key := range []string{"text", "content", "message", "markdown", "rawText", "richText", "humanReadableText"} {
		if text := strings.TrimSpace(cursorText(obj[key])); text != "" {
			return text
		}
	}
	return ""
}

func cursorText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, elem := range v {
			if text := strings.TrimSpace(cursorText(elem)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "value", "content", "markdown"} {
			if text := strings.TrimSpace(cursorText(v[key])); text != "" {
				return text
			}
		}
	}
	return ""
}

func cursorTime(obj map[string]any) time.Time {
	for _, key := range []string{"timestamp", "createdAt", "created_at", "time", "date"} {
		if t := parseCursorTime(obj[key]); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func parseCursorTime(value any) time.Time {
	switch v := value.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, v); err == nil {
				return t
			}
		}
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return unixCursorTime(n)
		}
	case float64:
		return unixCursorTime(int64(v))
	}
	return time.Time{}
}

func unixCursorTime(n int64) time.Time {
	switch {
	case n <= 0:
		return time.Time{}
	case n > 1_000_000_000_000:
		return time.UnixMilli(n)
	default:
		return time.Unix(n, 0)
	}
}

func cursorSessionFromCandidate(dbPath, workspace string, modTime time.Time, table, key string, candidate cursorCandidate) cass.Session {
	idHint := firstCursorString(candidate.parent, "composerId", "conversationId", "sessionId", "chatId", "id")
	session := cass.Session{
		ID:         cursorStableID(dbPath, table, key, candidate.pointer, idHint),
		Agent:      "cursor",
		Title:      cursorCandidateTitle(candidate.parent, candidate.messages, key),
		Workspace:  workspace,
		SourcePath: dbPath,
		Messages:   candidate.messages,
		Metadata: map[string]any{
			"storage_table": table,
			"storage_key":   key,
			"json_pointer":  candidate.pointer,
		},
	}
	if idHint != "" {
		session.Metadata["cursor_session_id"] = idHint
	}
	for _, msg := range session.Messages {
		if msg.Role == "user" {
			session.Stats.Turns++
		}
		if !msg.CreatedAt.IsZero() {
			if session.StartedAt.IsZero() || msg.CreatedAt.Before(session.StartedAt) {
				session.StartedAt = msg.CreatedAt
			}
			if session.EndedAt.IsZero() || msg.CreatedAt.After(session.EndedAt) {
				session.EndedAt = msg.CreatedAt
			}
		}
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = modTime
	}
	if session.EndedAt.IsZero() {
		session.EndedAt = modTime
	}
	if !session.StartedAt.IsZero() && !session.EndedAt.IsZero() {
		session.Stats.DurationSecs = int(session.EndedAt.Sub(session.StartedAt).Seconds())
	}
	return session
}

func cursorCandidateTitle(parent map[string]any, messages []cass.Message, key string) string {
	if title := firstCursorString(parent, "name", "title", "conversationTitle", "chatTitle"); title != "" {
		return truncateTitle(title)
	}
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		if title := firstCursorTitleLine(msg.Content); title != "" {
			return truncateTitle(title)
		}
	}
	return truncateTitle(key)
}

func firstCursorTitleLine(text string) string {
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

func cursorStableID(dbPath, table, key, pointer, hint string) string {
	raw := strings.Join([]string{dbPath, table, key, pointer, hint}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "cur-" + hex.EncodeToString(sum[:12])
}

type cursorWorkspaceFile struct {
	Folder    string `json:"folder"`
	Workspace string `json:"workspace"`
	Folders   []struct {
		Path string `json:"path"`
		URI  string `json:"uri"`
	} `json:"folders"`
}

func cursorWorkspace(dbPath string) string {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(dbPath), "workspace.json"))
	if err != nil {
		return ""
	}
	var ws cursorWorkspaceFile
	if err := json.Unmarshal(data, &ws); err != nil {
		return ""
	}
	for _, raw := range []string{ws.Folder, ws.Workspace} {
		if path := cleanCursorWorkspacePath(raw); path != "" {
			return path
		}
	}
	for _, folder := range ws.Folders {
		if path := cleanCursorWorkspacePath(folder.Path); path != "" {
			return path
		}
		if path := cleanCursorWorkspacePath(folder.URI); path != "" {
			return path
		}
	}
	return ""
}

func cleanCursorWorkspacePath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme == "file" {
		raw = u.Path
	}
	if raw == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			raw = home
		}
	} else if strings.HasPrefix(raw, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return raw
}

func cursorReadOnlyURI(path string) string {
	u := url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: "mode=ro&immutable=1",
	}
	return u.String()
}

func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func firstCursorString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(cursorString(obj[key])); s != "" {
			return s
		}
	}
	return ""
}

func cursorString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

func mapValue(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}
