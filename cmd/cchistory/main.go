package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

var (
	nFlag         = flag.Int("n", 100, "Show last N messages")
	localFlag     = flag.Bool("local", false, "Search only current directory")
	globalFlag    = flag.Bool("global", false, "Search all projects")
	sessionFlag   = flag.String("session", "", "Search specific session ID")
	sessionsFlag  = flag.String("sessions", "", "Path to sessions directory")
	sinceFlag     = flag.String("since", "7d", "Only search sessions modified within duration")
	commandsFlag  = flag.Bool("commands", false, "Search only user commands")
	responsesFlag = flag.Bool("responses", false, "Search only assistant responses")
	toolUseFlag   = flag.Bool("tool-use", false, "Search only tool use blocks")
	filesFlag     = flag.Bool("files", false, "Output only filenames")
	countFlag     = flag.Bool("count", false, "Show count of matches per file")
	contextFlag   = flag.Int("context", 0, "Show N messages of context")
	aFlag         = flag.Int("A", 0, "Show N messages after match")
	bFlag         = flag.Int("B", 0, "Show N messages before match")
	iFlag         = flag.Bool("i", false, "Case-insensitive search")
	formatFlag    = flag.String("format", "text", "Output format: text, json, compact")
	noFilename    = flag.Bool("no-filename", false, "Suppress filename prefixes")
)

// sessionMessage represents a session message.
type sessionMessage struct {
	Type    string          `json:"type"`
	Name    string          `json:"name,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message,omitempty"`
	raw string
}

// searchMatch represents a search result.
type searchMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cchistory: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Parse pattern from args
	var pattern string
	args := flag.Args()
	if len(args) > 0 {
		// Check if first arg is a number (like "chistory 50")
		if n, err := fmt.Sscanf(args[0], "%d", nFlag); n == 1 && err == nil {
			args = args[1:]
		}
		if len(args) > 0 {
			pattern = strings.Join(args, " ")
		}
	}

	// Compile regex if pattern provided
	var re *regexp.Regexp
	var err error
	if pattern != "" {
		if *iFlag {
			pattern = "(?i)" + pattern
		}
		re, err = regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern: %w", err)
		}
	}

	// Find session files
	files, err := findSessionFiles()
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no session files found")
	}

	// Collect all matches
	var allMatches []searchMatch
	fileCounts := make(map[string]int)

	for _, file := range files {
		matches, err := searchFile(file, re)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", file, err)
			continue
		}

		if *filesFlag && len(matches) > 0 {
			fmt.Println(file)
			continue
		}

		if *countFlag {
			fileCounts[file] = len(matches)
			continue
		}

		allMatches = append(allMatches, matches...)
	}

	if *countFlag {
		for file, count := range fileCounts {
			fmt.Printf("%s:%d\n", file, count)
		}
		return nil
	}

	if *filesFlag {
		return nil
	}

	// Limit results
	if len(allMatches) > *nFlag {
		allMatches = allMatches[len(allMatches)-*nFlag:]
	}

	// Output results
	return outputMatches(allMatches)
}

func findSessionFiles() ([]string, error) {
	var searchDirs []string

	if *sessionsFlag != "" {
		searchDirs = []string{*sessionsFlag}
	} else if *localFlag {
		searchDirs = []string{"."}
	} else if *globalFlag {
		searchDirs = getGlobalSearchDirs()
	} else {
		// Default: directory-aware (prefer project sessions)
		searchDirs = getProjectSearchDirs()
	}

	// Parse since duration
	since, err := parseDuration(*sinceFlag)
	if err != nil {
		return nil, fmt.Errorf("invalid duration: %w", err)
	}
	cutoff := time.Now().Add(-since)

	var files []string
	seen := make(map[string]bool)

	for _, dir := range searchDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip inaccessible directories
			}
			if info.IsDir() {
				// Skip hidden directories except .claude
				if strings.HasPrefix(info.Name(), ".") && info.Name() != ".claude" && info.Name() != ".sessions" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".ndjson") {
				return nil
			}
			if info.ModTime().Before(cutoff) {
				return nil
			}
			if *sessionFlag != "" && !strings.Contains(path, *sessionFlag) {
				return nil
			}

			absPath, _ := filepath.Abs(path)
			if !seen[absPath] {
				seen[absPath] = true
				files = append(files, path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	// Sort by modification time (newest first)
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		if fi == nil || fj == nil {
			return false
		}
		return fi.ModTime().After(fj.ModTime())
	})

	return files, nil
}

func getGlobalSearchDirs() []string {
	ch, _ := cc.ClaudeHome()
	home, _ := os.UserHomeDir()
	dirs := []string{
		".",
		".sessions",
		filepath.Join(ch, "sessions"),
		filepath.Join(home, ".config", "claude", "sessions"),
	}
	return dirs
}

func getProjectSearchDirs() []string {
	dirs := []string{"."}

	gitCtx, err := cc.ResolveGitContext("")
	if err == nil {
		dirs = append(dirs,
			filepath.Join(gitCtx.WorktreePath, ".sessions"),
			filepath.Join(gitCtx.WorktreePath, ".claude", "sessions"),
		)
	}

	ch, _ := cc.ClaudeHome()
	if ch != "" {
		if err == nil {
			hash := sha256.Sum256([]byte(gitCtx.GitCommonDir))
			hashStr := fmt.Sprintf("%x", hash)[:8]
			dirs = append(dirs, filepath.Join(ch, "sessions", hashStr))
		}
		dirs = append(dirs, filepath.Join(ch, "sessions"))
	}

	return dirs
}

func searchFile(path string, re *regexp.Regexp) ([]searchMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var messages []sessionMessage
	var matches []searchMatch
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg sessionMessage
		msg.raw = line
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // Skip malformed lines
		}
		messages = append(messages, msg)

		// Filter by message type
		if !matchesFilter(msg) {
			continue
		}

		// Extract searchable content
		content := extractContent(msg)

		// Match against pattern
		if re == nil || re.MatchString(content) {
			matches = append(matches, searchMatch{
				File:    path,
				Line:    lineNum,
				Type:    getMessageType(msg),
				Content: content,
			})
		}
	}

	// Add context if requested
	if (*contextFlag > 0 || *aFlag > 0 || *bFlag > 0) && len(matches) > 0 {
		matches = addContext(matches, messages, path)
	}

	return matches, scanner.Err()
}

func matchesFilter(msg sessionMessage) bool {
	if *commandsFlag {
		return msg.Type == "user" || (msg.Message != nil && msg.Message.Role == "user")
	}
	if *responsesFlag {
		return msg.Type == "assistant" || (msg.Message != nil && msg.Message.Role == "assistant")
	}
	if *toolUseFlag {
		return msg.Type == "tool_use"
	}
	return true
}

func getMessageType(msg sessionMessage) string {
	if msg.Message != nil {
		return msg.Message.Role
	}
	return msg.Type
}

func extractContent(msg sessionMessage) string {
	// Try message.content first
	if msg.Message != nil && msg.Message.Content != nil {
		return extractFromContent(msg.Message.Content)
	}
	// Fall back to content
	if msg.Content != nil {
		return extractFromContent(msg.Content)
	}
	// For tool_use, include name
	if msg.Name != "" {
		return msg.Name
	}
	return ""
}

func extractFromContent(raw json.RawMessage) string {
	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}

	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	return string(raw)
}

func addContext(matches []searchMatch, messages []sessionMessage, path string) []searchMatch {
	// Simplified context - just expand line ranges
	before := *bFlag
	after := *aFlag
	if *contextFlag > 0 {
		before = *contextFlag
		after = *contextFlag
	}

	seen := make(map[int]bool)
	var result []searchMatch

	for _, m := range matches {
		start := m.Line - before
		if start < 1 {
			start = 1
		}
		end := m.Line + after
		if end > len(messages) {
			end = len(messages)
		}

		for i := start; i <= end; i++ {
			if seen[i] {
				continue
			}
			seen[i] = true
			if i-1 < len(messages) {
				msg := messages[i-1]
				result = append(result, searchMatch{
					File:    path,
					Line:    i,
					Type:    getMessageType(msg),
					Content: extractContent(msg),
				})
			}
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Line < result[j].Line
	})

	return result
}

func outputMatches(matches []searchMatch) error {
	switch *formatFlag {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		for _, m := range matches {
			if err := enc.Encode(m); err != nil {
				return err
			}
		}
	case "compact":
		for _, m := range matches {
			content := strings.ReplaceAll(m.Content, "\n", " ")
			if len(content) > 100 {
				content = content[:100] + "..."
			}
			if *noFilename {
				fmt.Printf("%d:%s\n", m.Line, content)
			} else {
				fmt.Printf("%s:%d:%s\n", m.File, m.Line, content)
			}
		}
	default: // text
		for _, m := range matches {
			content := strings.TrimSpace(m.Content)
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			if *noFilename {
				fmt.Printf("[%s] %s\n", m.Type, content)
			} else {
				fmt.Printf("%s:%d: [%s] %s\n", m.File, m.Line, m.Type, content)
			}
		}
	}
	return nil
}

func parseDuration(s string) (time.Duration, error) {
	// Handle extended duration formats: 7d, 2w, 3m, 1y
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]

	var num int
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
		return time.ParseDuration(s)
	}

	switch suffix {
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(num) * 7 * 24 * time.Hour, nil
	case 'm':
		return time.Duration(num) * 30 * 24 * time.Hour, nil
	case 'y':
		return time.Duration(num) * 365 * 24 * time.Hour, nil
	default:
		return time.ParseDuration(s)
	}
}
