// Command ccloc prints the Claude Code agent cache location for a directory.
//
// # Usage
//
//	ccloc [directory]
//	ccloc                      # Cache location for current directory
//	ccloc /path/to/project     # Cache location for specific path
//
// The ccloc command returns the path where Claude Code stores agent
// cache data for a given directory. This is useful for scripting,
// debugging, and inspecting session data.
//
// # Output Format
//
// By default, prints the full path:
//
//	~/.claude/projects/-Volumes-tmc-go-src-github-com-tmc-cc/
//
// The path encoding replaces path separators with dashes.
//
// # Options
//
//	-check
//	    Check if the cache directory exists (exit 0 if yes, 1 if no)
//
//	-create
//	    Create the cache directory if it doesn't exist
//
//	-sessions
//	    Print the sessions subdirectory path
//
//	-expand
//	    Expand ~ to the actual home directory
//
// # Examples
//
// Get cache location for current directory:
//
//	ccloc
//
// Get cache location for specific project:
//
//	ccloc ~/go/src/github.com/tmc/appledocs
//
// Check if cache exists:
//
//	if ccloc -check; then
//	  echo "Cache exists"
//	fi
//
// Create cache directory:
//
//	ccloc -create
//
// Get sessions directory:
//
//	ccloc -sessions
//	# ~/.claude/projects/-path-to-project/sessions/
//
// Use in scripts:
//
//	CACHE=$(ccloc -expand)
//	ls "$CACHE"
//
// # Path Encoding
//
// Claude Code encodes directory paths by replacing separators:
//
//	/Volumes/tmc/go/src/github.com/tmc/cc
//	→ -Volumes-tmc-go-src-github-com-tmc-cc
//
// This ensures unique, filesystem-safe directory names.
package main
