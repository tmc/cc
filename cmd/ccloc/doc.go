// Command ccloc prints the Claude Code project cache directory for a path.
//
// Claude Code stores per-project session data under
// ~/.claude/projects/<encoded-path>/, where the encoding replaces path
// separators with dashes (`/Volumes/tmc/go` → `-Volumes-tmc-go`).
// ccloc resolves a directory to its cache path and optionally checks
// or creates it.
//
// # Usage
//
//	ccloc [flags] [DIR]
//
// DIR defaults to the current directory. The path is made absolute
// before encoding.
//
// # Flags
//
//	-check       Exit 0 if the cache directory exists, 1 otherwise.
//	-create      Create the cache directory if it does not exist.
//	-sessions    Print the `sessions` subdirectory of the cache path.
//	-short       Replace the leading $HOME with `~`.
//	-gemini      Resolve under ~/.gemini instead of ~/.claude.
//
// # Examples
//
// Print the cache path for the current directory:
//
//	ccloc
//
// Check whether a path has a cache and create one if not:
//
//	ccloc -check ~/work || ccloc -create ~/work
//
// Print the Gemini sessions subdirectory in shortened form:
//
//	ccloc -gemini -sessions -short
package main
