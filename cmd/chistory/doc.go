// Command chistory searches through Claude Code session history.
//
// # Usage
//
//	chistory [options] [pattern]
//	chistory                          # Show all history (like bash history)
//	chistory 50                       # Show last 50 entries
//	chistory "error handling"         # Search for pattern
//	chistory -local                   # Current directory only
//	chistory -session SID             # Specific session only
//
// The chistory command works like bash history but for Claude Code sessions.
// It shows commands and responses from session NDJSON files, with smart
// defaults for the current directory and time-based filtering.
//
// # Basic Usage (Like Bash History)
//
// Show recent history:
//
//	chistory           # All recent sessions
//	chistory 100       # Last 100 messages
//	chistory -n 50     # Last 50 messages (explicit)
//
// Search history:
//
//	chistory pattern   # Search all messages
//	!pattern           # Shell expansion (if configured)
//
// # Directory Awareness
//
// By default, chistory is directory-aware:
//
//   - Searches sessions for current git repo (via mksid hash)
//   - Falls back to CWD-based session discovery
//   - Use -global to search all sessions across all projects
//   - Use -local to restrict to current directory only
//
// This means running chistory in different projects shows relevant history.
//
// # Arguments
//
//	[N]
//	    Show last N messages (like history N)
//	    Example: chistory 50
//
//	pattern
//	    Search pattern (supports regex)
//	    If omitted, lists all recent messages
//
// # Options
//
//	-n int
//	    Show last N messages (default: 100)
//	    Like history -n in bash
//
//	-local
//	    Search only sessions in current directory
//	    Restricts to CWD and subdirectories
//
//	-global
//	    Search all sessions across all projects
//	    Overrides default git-repo filtering
//
//	-session string
//	    Search only in specific session ID
//	    Example: -session 20241115-abc123-def456
//
//	-sessions string
//	    Path to sessions directory (default: auto-discover)
//	    Searches for .ndjson files recursively
//
//	-since duration
//	    Only search sessions modified within duration
//	    Examples: 1h, 24h, 7d, 2w (default: 7d)
//
//	-before time
//	    Only search sessions before timestamp
//	    Format: RFC3339 or relative (e.g., "2024-11-15", "yesterday")
//
//	-after time
//	    Only search sessions after timestamp
//	    Format: RFC3339 or relative
//
//	-commands
//	    Search only in user commands (role: user)
//
//	-responses
//	    Search only in assistant responses (role: assistant)
//
//	-tool-use
//	    Search only in tool use blocks
//
//	-tool-results
//	    Search only in tool result blocks
//
//	-files
//	    Output only filenames (no content)
//	    Like grep -l
//
//	-count
//	    Show count of matches per file
//	    Like grep -c
//
//	-context int
//	    Show N messages of context before and after match
//	    Default: 0
//
//	-A int
//	    Show N messages after each match
//
//	-B int
//	    Show N messages before each match
//
//	-session-id string
//	    Search only in sessions matching ID pattern
//
//	-i
//	    Case-insensitive search
//
//	-v
//	    Invert match (show non-matching messages)
//
//	-format string
//	    Output format: text, json, compact (default: text)
//
//	-no-filename
//	    Suppress filename prefixes in output
//
//	-with-line-numbers
//	    Include line numbers in output (message index)
//
// # Session Discovery
//
// Default behavior (directory-aware):
//
//  1. Detect current git repository (if any)
//  2. Find sessions with matching git hash (from mksid)
//  3. Search in standard session directories
//  4. Filter to relevant project history
//
// The command discovers session files using:
//
//   - Git repo hash from mksid (for project isolation)
//   - Explicit -sessions path if provided
//   - $CLAUDE_SESSIONS environment variable
//   - Standard locations:
//     ~/.claude/sessions/
//     ~/.config/claude/sessions/
//     ./.claude-sessions/
//   - Current directory (recursive .ndjson search)
//
// Use -local to restrict to CWD only.
// Use -global to search across all projects.
//
// # Search Behavior
//
// By default, chistory searches all message content:
//
//   - User messages (commands)
//   - Assistant messages (responses)
//   - Tool use blocks
//   - Tool result blocks
//
// Use filter flags (-commands, -responses, etc.) to narrow search scope.
//
// # Output Format
//
// Default text output format:
//
//	session-20241115-abc123.ndjson:5: [user] Can you help with error handling?
//	session-20241115-abc123.ndjson:6: [assistant] I'll help you implement proper error handling.
//
// With -format json:
//
//	{
//	  "file": "session-20241115-abc123.ndjson",
//	  "line": 5,
//	  "type": "user",
//	  "content": "Can you help with error handling?",
//	  "timestamp": "2024-11-15T10:30:00Z"
//	}
//
// Compact format (one line per match):
//
//	session-20241115-abc123.ndjson:5:Can you help with error handling?
//
// # Examples
//
// Basic usage (like bash history):
//
//	chistory              # Show recent history for current project
//	chistory 50           # Show last 50 messages
//	chistory "git"        # Search for "git" in history
//
// Directory-aware searches:
//
//	chistory -local       # Current directory only
//	chistory -global      # All projects
//	cd /other/project && chistory  # Different project's history
//
// Focus on specific session:
//
//	chistory -session 20241115-abc123-def456
//	chistory -session $(ls -t *.ndjson | head -1)
//
// Search for patterns:
//
//	chistory "authentication"
//	chistory -commands "git commit"
//	chistory -responses database
//
// Time-based filtering:
//
//	chistory -since 1h          # Last hour
//	chistory -since 24h "error" # Last day's errors
//	chistory -n 1000            # Last 1000 messages
//
// Output formats:
//
//	chistory -files             # List session files
//	chistory -count "TODO"      # Count matches
//	chistory -format json       # JSON output
//
// With context (like grep):
//
//	chistory -context 2 "error occurred"
//	chistory -A 3 "function"    # Show 3 after
//	chistory -B 2 "error"       # Show 2 before
//
// Search specific content types:
//
//	chistory -tool-use "Read"
//	chistory -i -responses "error|failed|exception"
//
// Combined filters:
//
//	chistory -local -since 24h -commands "git"
//	chistory -session SID -responses -i "todo"
//
// # Time Filters
//
// Time-based filtering supports various formats:
//
//   - Duration: 1h, 24h, 7d, 2w, 3m, 1y
//   - Absolute: 2024-11-15, 2024-11-15T10:30:00Z
//   - Relative: today, yesterday, last-week
//
// # Message Types
//
// NDJSON session files contain structured messages:
//
//	{"type":"user","message":{"role":"user","content":[...]}}
//	{"type":"assistant","message":{"role":"assistant","content":[...]}}
//	{"type":"tool_use","name":"Read","input":{...}}
//	{"type":"tool_result","tool_use_id":"...","content":"..."}
//
// The search handles all content types intelligently.
//
// # Streaming Compatibility
//
// The command handles:
//
//   - Incomplete sessions (partial NDJSON)
//   - Malformed messages (skips with warning)
//   - Large session files (streaming read)
//   - Concurrent access to active sessions
//
// # Performance
//
// For large session directories:
//
//   - Use -since to limit time range
//   - Use -session-id to filter by ID pattern
//   - Use -files to avoid reading full content
//   - Searches are parallelized across files
//
// # Integration
//
// The chistory command integrates with other cc utilities:
//
//   - Search sessions created by cmsg
//   - Find sessions by mksid-generated IDs
//   - Locate sessions for replay with creplay
//   - Export results for further processing
//
// # Exit Status
//
//	0   One or more matches found
//	1   No matches found
//	2   Error occurred
//
// # Examples with Other Tools
//
// Find and replay recent session:
//
//	chistory -files -n 1 | creplay -file
//	SID=$(chistory -files | head -1)
//	creplay -file "$SID"
//
// Re-run last command (like !!):
//
//	chistory -commands -n 1 --no-filename
//
// Review today's work:
//
//	chistory -since 1d | less
//
// Count commands per day:
//
//	chistory -commands -since 30d -format json | jq -r '.timestamp[:10]' | sort | uniq -c
//
// Extract all tool uses:
//
//	chistory -tool-use ".*" -format json | jq -r '.name'
//
// Find sessions with errors:
//
//	chistory -i "error|failed|exception" -files | xargs -I {} echo "Review: {}"
//
// History per project:
//
//	cd ~/project1 && chistory -n 50  # Project 1 history
//	cd ~/project2 && chistory -n 50  # Project 2 history
//
// # Advanced Patterns
//
// Regex patterns support full Go regexp syntax:
//
//	chistory "func.*\{.*\}"           # Find function definitions
//	chistory "git (commit|push|pull)" # Find git operations
//	chistory "(?i)todo|fixme"         # Case-insensitive markers
//	chistory "^import\s"              # Lines starting with import
//
// # Output Redirection
//
// Results can be piped or redirected:
//
//	chistory pattern > results.txt
//	chistory -format json pattern | jq '.content'
//	chistory pattern | grep -v "noise"
package main
