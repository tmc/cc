// Command ccmemory lists and reads Claude Code auto-memory files.
//
// Auto-memory is stored at
// ~/.claude/projects/<encoded-path>/memory/, with an index in MEMORY.md
// and one .md file per memory entry.
//
// Usage:
//
//	ccmemory                      # list memories for current project
//	ccmemory -show NAME           # print one memory file
//	ccmemory -project DIR         # target a different project
//	ccmemory -index               # print MEMORY.md
//	ccmemory -path                # print the memory directory path
package main
