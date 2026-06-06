// Command cchandoff builds a cross-tool handoff prompt from a prior session.
//
// It reads a Claude Code JSONL session, opencode session JSON file, or a Gemini chat JSON file and
// produces a structured bootstrap prompt for continuing the work in a
// different tool (claude-code, gemini-cli, codex, opencode, ...). The prompt
// summarizes the most recent user ask, recent conversation, files
// touched, recent shell commands, and an optional git snapshot.
//
// # Usage
//
//	cchandoff -from SESSION [flags]
//
// # Flags
//
//	-from PATH      Source session file (.jsonl, opencode .json, or Gemini session-*.json).
//	-to TOOL        Target tool: gemini|gemini-cli|claude|claude-code|codex|codex-cli|codex-app|opencode.
//	-workspace DIR  Override workspace path.
//	-context N      Recent conversation lines to include.
//	-commands N     Recent shell commands to include.
//	-files N        Maximum files to include.
//	-git            Include git branch and status snapshot.
//	-json           Output machine-readable JSON.
//	-out PATH       Write output to PATH instead of stdout.
package main
