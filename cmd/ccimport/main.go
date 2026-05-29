package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

type geminiChatFile struct {
	SessionID   string              `json:"sessionId"`
	ProjectHash string              `json:"projectHash"`
	StartTime   string              `json:"startTime"`
	LastUpdated string              `json:"lastUpdated"`
	Messages    []geminiChatMessage `json:"messages"`
}

type geminiChatMessage struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Content   any    `json:"content"`
	Model     string `json:"model,omitempty"`
	Tokens    struct {
		Input  int `json:"input"`
		Output int `json:"output"`
		Cached int `json:"cached"`
	} `json:"tokens,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ccimport:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		from      = flag.String("from", "", "Claude session JSONL file to import")
		toProject = flag.String("to-project", "", "Target project path in Gemini (required unless -out is set)")
		out       = flag.String("out", "", "Output session JSON path (optional)")
		dryRun    = flag.Bool("dry-run", false, "Print output path/summary without writing file")
	)
	flag.Parse()

	if *from == "" {
		return fmt.Errorf("missing -from")
	}
	entries, err := cc.ReadFile(context.Background(), *from)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("empty source session: %s", *from)
	}

	msgs, drops := toGeminiMessages(entries)
	if len(msgs) == 0 {
		return fmt.Errorf("no importable user/assistant messages found")
	}
	drops.warn(os.Stderr)

	sessionID := entries[0].SessionID
	if sessionID == "" {
		sessionID = stableID(*from)
	}
	projectPath := *toProject
	if projectPath == "" && *out == "" {
		return fmt.Errorf("missing -to-project or -out")
	}
	projectHash := ""
	if projectPath != "" {
		sum := sha256.Sum256([]byte(projectPath))
		projectHash = fmt.Sprintf("%x", sum[:])
	}

	chat := geminiChatFile{
		SessionID:   sessionID,
		ProjectHash: projectHash,
		StartTime:   msgs[0].Timestamp,
		LastUpdated: msgs[len(msgs)-1].Timestamp,
		Messages:    msgs,
	}

	outPath := *out
	if outPath == "" {
		p, err := defaultGeminiOutPath(projectPath, sessionID, chat.StartTime)
		if err != nil {
			return err
		}
		outPath = p
	}

	if *dryRun {
		fmt.Printf("source: %s\n", *from)
		if projectPath != "" {
			fmt.Printf("project: %s\n", projectPath)
		}
		fmt.Printf("output: %s\n", outPath)
		fmt.Printf("messages: %d\n", len(chat.Messages))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if projectPath != "" {
		_ = os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(outPath)), ".project_root"), []byte(projectPath+"\n"), 0o644)
	}

	b, err := json.MarshalIndent(chat, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	fmt.Println(outPath)
	return nil
}

// dropCounts tracks Claude concepts that the Gemini session format cannot
// represent. Silent drops violate TRACE_INTEROP principle 3, so counts
// get summarized to stderr.
type dropCounts struct {
	toolUses    int
	toolResults int
	subagents   int
	emptyText   int
}

func (d dropCounts) warn(w io.Writer) {
	if d.toolUses == 0 && d.toolResults == 0 && d.subagents == 0 && d.emptyText == 0 {
		return
	}
	fmt.Fprintln(w, "ccimport: warning: gemini session format cannot represent:")
	if d.toolUses > 0 {
		fmt.Fprintf(w, "  %d tool_use blocks dropped\n", d.toolUses)
	}
	if d.toolResults > 0 {
		fmt.Fprintf(w, "  %d tool_result blocks dropped\n", d.toolResults)
	}
	if d.subagents > 0 {
		fmt.Fprintf(w, "  %d subagent entries dropped (isSidechain=true)\n", d.subagents)
	}
	if d.emptyText > 0 {
		fmt.Fprintf(w, "  %d user/assistant entries had no text after extraction\n", d.emptyText)
	}
}

func toGeminiMessages(entries []cc.Entry) ([]geminiChatMessage, dropCounts) {
	var out []geminiChatMessage
	var drops dropCounts
	for i, e := range entries {
		if e.IsSidechain {
			drops.subagents++
			continue
		}
		if e.Message == nil {
			continue
		}
		role := e.Message.Role
		if role != "user" && role != "assistant" {
			continue
		}
		drops.toolUses += len(e.Message.ToolUses())
		drops.toolResults += len(e.Message.ToolResults())

		text := strings.TrimSpace(e.Message.TextContent())
		if text == "" {
			drops.emptyText++
			continue
		}

		m := geminiChatMessage{
			ID:        e.UUID,
			Timestamp: formatTS(e.Timestamp),
		}
		if m.ID == "" {
			m.ID = fmt.Sprintf("m-%d", i+1)
		}
		if role == "user" {
			m.Type = "user"
			// Match modern Gemini format where user content is an array of text parts.
			m.Content = []map[string]string{{"text": text}}
			if e.Message.Usage != nil {
				m.Tokens.Input = e.Message.Usage.InputTokens
				m.Tokens.Output = e.Message.Usage.OutputTokens
				m.Tokens.Cached = e.Message.Usage.CacheReadInputTokens
			}
		} else {
			m.Type = "gemini"
			m.Content = text
			m.Model = e.Message.Model
			if e.Message.Usage != nil {
				m.Tokens.Input = e.Message.Usage.InputTokens
				m.Tokens.Output = e.Message.Usage.OutputTokens
				m.Tokens.Cached = e.Message.Usage.CacheReadInputTokens
			}
		}
		out = append(out, m)
	}
	return out, drops
}

func formatTS(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return t.UTC().Format(time.RFC3339)
}

func stableID(s string) string {
	sum := sha256.Sum256([]byte(s))
	hex := fmt.Sprintf("%x", sum[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex[0:8], hex[8:12], hex[12:16], hex[16:20], hex[20:32])
}

func defaultGeminiOutPath(projectPath, sessionID, startTS string) (string, error) {
	gh, err := ccpaths.GeminiHome()
	if err != nil {
		return "", fmt.Errorf("gemini home: %w", err)
	}
	name, err := projectName(projectPath)
	if err != nil {
		return "", err
	}
	short := strings.ReplaceAll(sessionID, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	t := time.Now().UTC()
	if ts, err := time.Parse(time.RFC3339, startTS); err == nil {
		t = ts.UTC()
	}
	file := fmt.Sprintf("session-%s-%s.json", t.Format("2006-01-02T15-04"), short)
	return filepath.Join(gh, "tmp", name, "chats", file), nil
}

func projectName(projectPath string) (string, error) {
	if strings.TrimSpace(projectPath) == "" {
		return "", fmt.Errorf("missing project path")
	}
	gh, err := ccpaths.GeminiHome()
	if err != nil {
		return "", fmt.Errorf("gemini home: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(gh, "projects.json"))
	if err == nil {
		var cfg struct {
			Projects map[string]string `json:"projects"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			if n := cfg.Projects[projectPath]; n != "" {
				return n, nil
			}
		}
	}
	return filepath.Base(projectPath), nil
}
