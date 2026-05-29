// Package ccpaths resolves the Claude, Gemini, and Codex home directories and
// encodes or decodes Claude Code's project-directory path format.
//
// # Home directories
//
// [ClaudeHome], [GeminiHome], and [CodexHome] honor the CLAUDE_HOME,
// GEMINI_HOME, and CODEX_HOME environment variables, falling back to ~/.claude,
// ~/.gemini, and ~/.codex respectively.
//
// # Path encoding
//
// Claude Code names a project's session directory by replacing "/" and "." with
// "-". [EncodePath] applies that encoding; [DecodeSegments] recovers the
// original path by probing the filesystem:
//
//	enc := ccpaths.EncodePath("/Volumes/tmc/go/src/github.com/tmc/cc")
//	// -Volumes-tmc-go-src-github-com-tmc-cc
//
// [ShortPath] abbreviates the home directory to "~", and [ParseDuration] extends
// time.ParseDuration with "d" (day) and "w" (week) suffixes.
package ccpaths
