// Command ccconfig reads and writes Claude Code configuration values.
//
// ccconfig manages key=value settings stored in INI-style files at three
// scopes: local (./.claude/config), project (<git-worktree>/.claude/config),
// and global (~/.claude/config). With the -gemini flag, the .gemini
// directory is used instead.
//
// # Usage
//
//	ccconfig [scope] <key>             Print the value for key.
//	ccconfig [scope] <key> <value>     Set key to value.
//	ccconfig [scope] -unset <key>      Remove key.
//	ccconfig [scope] -list             Print all merged settings.
//	ccconfig [scope] -edit             Print the path of the chosen file.
//
// Scope flags select which file is read or written:
//
//	-local      ./.claude/config
//	-project    <worktree>/.claude/config (requires a git repo)
//	-global     ~/.claude/config (default for writes)
//
// Without a scope flag, reads merge all three files in priority order
// (local > project > global) and writes go to the global file.
//
// # Flags
//
//	-list, -l      List merged settings as key=value pairs.
//	-get           Treat arg as a key to read (implicit when one arg given).
//	-unset         Remove a key from the chosen scope.
//	-show-origin   Prefix output with the file each value came from.
//	-edit          Print the chosen config path (does not invoke an editor).
//	-file PATH     Read or write a specific file instead of a scoped path.
//	-gemini        Use ~/.gemini and .gemini paths instead of .claude.
//
// # File Format
//
// Files are INI-like: optional `[section]` headers and `key = value`
// lines. Section headers are joined to keys with a dot, so
//
//	[sessions]
//	path = ~/claude-sessions
//
// is equivalent to the key `sessions.path`. Comments begin with `#` or
// `;`. Values containing spaces are quoted on write.
//
// # Examples
//
// Print the global value for sessions.path:
//
//	ccconfig sessions.path
//
// Set a project-scoped value:
//
//	ccconfig -project history.limit 500
//
// List every effective setting with its source file:
//
//	ccconfig -list -show-origin
//
// Remove a key from the local config:
//
//	ccconfig -local -unset history.limit
package main
