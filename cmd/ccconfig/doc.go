// Command ccconfig manages Claude Code configuration.
//
// # Usage
//
//	ccconfig [options] <key> [value]
//	ccconfig --list                    # List all config
//	ccconfig --get key                 # Get a value
//	ccconfig key value                 # Set a value
//	ccconfig --unset key               # Remove a key
//
// The ccconfig command works like git config, managing configuration at
// three levels: local (directory), project (git repo), and global (user).
//
// # Configuration Levels
//
// Settings are resolved in priority order:
//
//  1. Local (./.claude/config)      - Highest priority
//  2. Project (.git/.claude/config) - Git repository level
//  3. Global (~/.claude/config)     - User-wide defaults
//
// This allows per-directory overrides while maintaining sensible defaults.
//
// # Basic Usage
//
// Get a config value:
//
//	ccconfig sessions.path
//	ccconfig --get sessions.path
//
// Set a config value:
//
//	ccconfig sessions.path ~/my-sessions
//	ccconfig user.name "Alice"
//
// List all configuration:
//
//	ccconfig --list
//	ccconfig -l
//
// Remove a setting:
//
//	ccconfig --unset sessions.path
//
// # Scope Options
//
//	--local
//	    Use local config (./.claude/config)
//	    Affects current directory only
//
//	--project
//	    Use project config (.git/.claude/config)
//	    Affects git repository
//
//	--global
//	    Use global config (~/.claude/config)
//	    User-wide settings (default for set operations)
//
// # Common Settings
//
// Session management:
//
//	sessions.path          Path to sessions directory
//	sessions.retention     How long to keep sessions (e.g., 30d)
//	sessions.autoname      Auto-generate session names
//
// History behavior:
//
//	history.limit          Default number of entries to show
//	history.format         Output format (text, json, compact)
//	history.scope          Default scope (local, project, global)
//
// Replay preferences:
//
//	replay.speed           Default playback speed
//	replay.follow          Enable follow mode by default
//	replay.theme           Color theme for TUI
//
// Message formatting:
//
//	msg.template           Default message template
//	msg.type               Default message type
//
// User information:
//
//	user.name              Your name (for git notes)
//	user.email             Your email
//
// Advanced:
//
//	core.editor            Text editor to use
//	core.pager             Pager for long output
//	diff.tool              Diff tool for session comparison
//
// # Examples
//
// Set global session path:
//
//	ccconfig --global sessions.path ~/claude-sessions
//
// Set project-specific retention:
//
//	ccconfig --project sessions.retention 90d
//
// Set local history limit:
//
//	ccconfig --local history.limit 500
//
// List all settings with their sources:
//
//	ccconfig --list --show-origin
//
// Get effective value (respecting precedence):
//
//	ccconfig sessions.path
//
// Set user info globally:
//
//	ccconfig --global user.name "Alice Developer"
//	ccconfig --global user.email "alice@example.com"
//
// Configure replay defaults:
//
//	ccconfig --global replay.speed 1.5
//	ccconfig --global replay.theme "dracula"
//
// # Configuration File Format
//
// Config files use TOML format:
//
//	# ~/.claude/config
//	[sessions]
//	path = "~/claude-sessions"
//	retention = "30d"
//	autoname = true
//
//	[history]
//	limit = 100
//	format = "text"
//	scope = "project"
//
//	[user]
//	name = "Alice Developer"
//	email = "alice@example.com"
//
//	[replay]
//	speed = 1.0
//	follow = false
//	theme = "default"
//
// # Scope Resolution
//
// When reading config without --local/--project/--global:
//
//  1. Check ./.claude/config (local)
//  2. Check .git/.claude/config (project)
//  3. Check ~/.claude/config (global)
//  4. Use built-in defaults
//
// When writing config without scope flag:
//
//   - Defaults to --global
//   - Use explicit flags to override
//
// # Advanced Options
//
//	--list, -l
//	    List all configuration
//
//	--get
//	    Get value for key
//
//	--get-all
//	    Get all values for multi-value key
//
//	--add
//	    Add a new value (for multi-value keys)
//
//	--unset
//	    Remove a key
//
//	--unset-all
//	    Remove all values for a key
//
//	--show-origin
//	    Show which file each config value comes from
//
//	--show-scope
//	    Show scope (local/project/global) for each value
//
//	--edit
//	    Open config file in editor
//
//	--file path
//	    Use specific config file
//
//	--type type
//	    Ensure value is of type (bool, int, string, path)
//
// # Examples with Scope
//
// Different scopes for same key:
//
//	ccconfig --global history.limit 100   # User default
//	ccconfig --project history.limit 500  # This repo
//	ccconfig --local history.limit 1000   # This directory
//
// Check which value is active:
//
//	ccconfig history.limit                # Shows 1000 (local wins)
//	ccconfig --show-origin history.limit  # Shows where it's from
//
// List config from specific scope:
//
//	ccconfig --global --list
//	ccconfig --project --list
//	ccconfig --local --list
//
// # Editing Config Files
//
// Open in editor:
//
//	ccconfig --global --edit
//	ccconfig --project --edit
//	ccconfig --local --edit
//
// Uses $EDITOR or falls back to sensible defaults.
//
// # Validation
//
// Config values are validated on set:
//
//	ccconfig sessions.retention "invalid"  # Error: invalid duration
//	ccconfig replay.speed "abc"            # Error: not a number
//	ccconfig --type bool replay.follow "maybe"  # Error: not a bool
//
// Valid formats:
//
//   - Duration: 1h, 24h, 7d, 30d, 1y
//   - Bool: true, false, yes, no, 1, 0
//   - Int: 0, 1, 100, -1
//   - Path: ~/path, /absolute/path, relative/path
//
// # Migration
//
// Import from environment variables:
//
//	ccconfig --import-env
//
// Migrates:
//
//	CLAUDE_SESSIONS → sessions.path
//	CLAUDE_HISTORY_LIMIT → history.limit
//	etc.
//
// Export to environment:
//
//	eval $(ccconfig --export)
//
// # Integration Examples
//
// Use in scripts:
//
//	#!/bin/bash
//	SESSION_PATH=$(ccconfig sessions.path)
//	LIMIT=$(ccconfig history.limit)
//	chistory -n "$LIMIT" -sessions "$SESSION_PATH"
//
// Check if setting exists:
//
//	if ccconfig --get sessions.path &>/dev/null; then
//	  echo "Sessions path is configured"
//	fi
//
// Conditional execution:
//
//	if [ "$(ccconfig replay.follow)" = "true" ]; then
//	  cctl replay -follow "$SID"
//	else
//	  cctl replay "$SID"
//	fi
//
// # Per-Project Configuration
//
// Example workflow:
//
//	cd ~/project1
//	ccconfig --project sessions.path ./.sessions
//	ccconfig --project history.limit 1000
//
//	cd ~/project2
//	ccconfig --project sessions.path /shared/sessions
//	ccconfig --project sessions.retention 7d
//
// Each project maintains independent settings.
//
// # Default Values
//
// If not configured, defaults are:
//
//	sessions.path          ~/.claude/sessions
//	sessions.retention     30d
//	sessions.autoname      true
//	history.limit          100
//	history.format         text
//	history.scope          project
//	replay.speed           1.0
//	replay.follow          false
//	replay.theme           default
//
// # Exit Codes
//
//	0   Success
//	1   Key not found (for --get)
//	2   Invalid configuration
//	3   File error
//
// # Examples with Other Tools
//
// Configure and use chistory:
//
//	ccconfig --global history.limit 500
//	ccconfig --global history.format json
//	chistory pattern  # Uses configured defaults
//
// Configure replay behavior:
//
//	ccconfig --global replay.speed 2.0
//	ccconfig --global replay.follow true
//	cctl replay $SID  # Uses configured defaults
//
// Per-project session management:
//
//	cd ~/work-project
//	ccconfig --project sessions.path ~/work-sessions
//	cctl msg "work query" > session.ndjson
//	# Saved to ~/work-sessions/
//
//	cd ~/personal-project
//	ccconfig --project sessions.path ~/personal-sessions
//	cctl msg "personal query" > session.ndjson
//	# Saved to ~/personal-sessions/
//
// # File Locations
//
//	~/.claude/config              Global config
//	.git/.claude/config           Project config (git repo)
//	./.claude/config              Local config (current dir)
//	$XDG_CONFIG_HOME/claude/config  Alternative global
//
// # See Also
//
//   - git config (inspiration for this command)
//   - Other cctl subcommands use these settings
//   - Environment variables can override config
package main
