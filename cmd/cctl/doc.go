// Command cctl is the unified control command for Claude Code utilities.
//
// # Usage
//
//	cctl <subcommand> [options] [args]
//	cctl msg "hello"
//	cctl replay SESSION_ID
//	cctl sid
//	cctl history -since 24h pattern
//
// The cctl command provides a single entry point for all cc utilities.
// Subcommands are embedded as Go tools, keeping the core lean while
// providing a unified interface.
//
// # Subcommands
//
//	msg         Format stdin as Claude messages (cmsg)
//	replay      Replay Claude Code sessions in TUI (creplay)
//	sid         Generate session IDs (mksid)
//	history     Search session history (chistory)
//	version     Show version information
//	help        Show help for a subcommand
//
// # Architecture
//
// The cctl command is a thin dispatcher that embeds other commands:
//
//   - Core binary is minimal (routing logic only)
//   - Each subcommand is imported as a Go package
//   - Subcommands export a Run(args []string) error function
//   - No duplication - same code as standalone binaries
//
// # Subcommand Execution
//
// When you run:
//
//	cctl msg "hello"
//
// The cctl dispatcher:
//
//  1. Parses "msg" as the subcommand
//  2. Imports the cmsg package
//  3. Calls cmsg.Run([]string{"hello"})
//  4. Exits with the subcommand's exit code
//
// # Standalone vs Embedded
//
// All commands work both ways:
//
//	cmsg "hello"          # Standalone binary
//	cctl msg "hello"      # Embedded via cctl
//
// Both execute identical code. The standalone binaries are just
// thin wrappers around the same Run() function.
//
// # Why Embedded?
//
// Benefits of the embedded approach:
//
//   - Single binary to install and distribute
//   - Consistent versioning across all tools
//   - Smaller total disk footprint
//   - Easier to manage in containers/deployments
//   - Natural command hierarchy
//
// # Command Aliases
//
// Short aliases are available for convenience:
//
//	cctl m       → cctl msg
//	cctl r       → cctl replay
//	cctl s       → cctl sid
//	cctl h       → cctl history
//
// # Examples
//
// Format a message:
//
//	echo "Hello" | cctl msg
//	echo "Code review" | cctl msg -template "Review: {{.}}"
//
// Generate a session ID:
//
//	cctl sid
//	cctl sid -format uuid-only
//
// Replay a session:
//
//	cctl replay 20241115-abc123-def456
//	cctl replay -file session.ndjson -follow
//
// Search history:
//
//	cctl history -since 24h "error"
//	cctl history -commands "git commit"
//
// Chain commands together:
//
//	SID=$(cctl sid)
//	echo "query" | cctl msg | tee session-$SID.ndjson
//	cctl replay -follow $SID
//
// # Help System
//
// Get help for any subcommand:
//
//	cctl help msg
//	cctl help replay
//	cctl msg --help
//	cctl replay -h
//
// # Version Information
//
//	cctl version
//	cctl version --verbose
//
// Shows version info for cctl and all embedded commands.
//
// # Environment Variables
//
//	CCTL_SUBCOMMAND_PATH
//	    Override path for subcommand binaries (advanced)
//	    Allows using external implementations
//
// # Building
//
// Build cctl with all subcommands:
//
//	go build ./cmd/cctl
//
// This produces a single binary containing all tools.
//
// Build standalone commands:
//
//	go build ./cmd/cmsg
//	go build ./cmd/creplay
//	go build ./cmd/mksid
//	go build ./cmd/chistory
//
// Both approaches use the same core implementation.
//
// # Package Structure
//
// Lean architecture:
//
//	cmd/cctl/main.go           # Dispatcher only (~100 lines)
//	cmd/cmsg/main.go           # Thin wrapper
//	cmd/creplay/main.go        # Thin wrapper
//	internal/cmsg/cmsg.go      # Actual implementation
//	internal/creplay/creplay.go # Actual implementation
//
// Or simpler approach (current):
//
//	cmd/cctl/main.go           # Dispatcher + embedding
//	cmd/cmsg/main.go           # Standalone + exportable Run()
//	cmd/creplay/main.go        # Standalone + exportable Run()
//
// # Exit Codes
//
//	0   Success
//	1   Subcommand failure
//	2   Invalid subcommand or usage error
//
// # Performance
//
// The embedded approach adds negligible overhead:
//
//   - No exec() calls for subcommands
//   - Direct function invocation
//   - Shared process space
//   - Same performance as standalone binaries
//
// # Integration Examples
//
// Use in scripts:
//
//	#!/bin/bash
//	SID=$(cctl sid)
//
//	while IFS= read -r line; do
//	  echo "$line" | cctl msg >> "session-$SID.ndjson"
//	done
//
//	cctl replay -follow "$SID"
//
// Use in makefiles:
//
//	session:
//		$(eval SID := $(shell cctl sid))
//		@echo "Starting session $(SID)"
//		@cctl replay -follow $(SID) &
//
// Use in containers:
//
//	FROM golang:1.21 as builder
//	COPY . .
//	RUN go build ./cmd/cctl
//
//	FROM alpine:latest
//	COPY --from=builder /app/cctl /usr/local/bin/
//	# Single binary, all tools available
//
// # Extensibility
//
// Add new subcommands by:
//
//  1. Creating cmd/newtool with Run() function
//  2. Importing in cmd/cctl/main.go
//  3. Adding to subcommand map
//
// No changes to core logic required.
//
// # Comparison with Other Tools
//
// Similar to:
//
//   - git (git commit, git push, etc.)
//   - kubectl (kubectl get, kubectl apply, etc.)
//   - docker (docker run, docker build, etc.)
//
// But leaner:
//
//   - No plugin system (not needed)
//   - No configuration files (pure CLI)
//   - No daemon (stateless execution)
//   - Minimal dependencies
//
// # Design Philosophy
//
// Keep the core lean:
//
//   - Dispatcher logic only in cctl
//   - All business logic in subcommands
//   - No shared state between invocations
//   - Each subcommand is independent
//   - Composable via Unix pipes
//
// # Future Subcommands
//
// Potential additions:
//
//	cctl session      # Manage active sessions
//	cctl export       # Export sessions to different formats
//	cctl stats        # Session statistics and analytics
//	cctl clean        # Clean up old sessions
//
// All following the same lean, embedded pattern.
package main
