# cc

[![Go Reference](https://pkg.go.dev/badge/github.com/tmc/cc.svg)](https://pkg.go.dev/github.com/tmc/cc)

A Go toolkit for working with Claude Code session data.

`cc` provides a small library for parsing Claude Code (and Codex)
session JSONL plus a collection of CLI tools for searching, replaying,
formatting, and indexing those sessions.

## Install

```
go install github.com/tmc/cc/cmd/...@latest
```

This installs every binary in `cmd/`. To install just one:

```
go install github.com/tmc/cc/cmd/ccfmt@latest
```

## Quickstart

```sh
# Show the most recent sessions for the current project.
ccsessions

# Format a session as readable markdown.
ccfmt -format markdown ~/.claude/projects/<encoded>/sid.jsonl > session.md

# Resume a recent session matching a query.
ccresume "kafka rebalance"

# Search every indexed session across every agent.
cass index
cass search "retry policy"
```

## Library

The `cc` package gives you an [`Entry`](https://pkg.go.dev/github.com/tmc/cc#Entry)
stream over a session JSONL plus helpers for summarization and git
worktree resolution:

```go
entries, err := cc.ReadFile("session.jsonl")
if err != nil { ... }

s := cc.Summarize("session.jsonl", entries)
fmt.Println(s.Branch, s.GitCommonDir, s.DistinctCWDs)
```

See the [pkg.go.dev reference](https://pkg.go.dev/github.com/tmc/cc) for
the full API.

## Subpackages

- [`cc`](https://pkg.go.dev/github.com/tmc/cc) — core types and reader
  for Claude Code / Codex session JSONL (Entry, Message, Reader,
  Summarize, GitContext, subagent + task-notification parsers).
- [`cass`](https://pkg.go.dev/github.com/tmc/cc/cass) — Coding Agent
  Session Search: a SQLite FTS5 index over sessions from multiple
  agents, with collectors, store, and a service layer.

## Commands

### Session viewing and formatting

| Command | Description |
|---|---|
| [`ccsessions`](cmd/ccsessions) | List and summarize Claude Code sessions. |
| [`ccfmt`](cmd/ccfmt) | Format coding session transcripts (text or markdown). |
| [`creplay`](cmd/creplay) | Replay a Claude Code session in a terminal UI. |
| [`cccat`](cmd/cccat) | Filter and display Claude Code session entries. |
| [`cchistory`](cmd/cchistory) | Search through Claude Code session history. |
| [`cctime`](cmd/cctime) | Show a timeline of events in a Claude Code session. |

### Search, resume, indexing

| Command | Description |
|---|---|
| [`cass`](cmd/cass) | Index and search AI coding-agent session history (Claude Code, Codex, etc.). |
| [`ccresume`](cmd/ccresume) | Find a recent Claude Code session and resume it. |
| [`ccstats`](cmd/ccstats) | Report token usage, tool counts, and timing for sessions. |
| [`ccloc`](cmd/ccloc) | Print the Claude Code agent cache location for a directory. |

### Inspection and analysis

| Command | Description |
|---|---|
| [`ccdiff`](cmd/ccdiff) | Show file changes made during Claude Code sessions. |
| [`ccfiles`](cmd/ccfiles) | Extract file operations from Claude Code sessions. |
| [`ccerr`](cmd/ccerr) | Find errors, failures, and retries in Claude Code sessions. |
| [`cctool`](cmd/cctool) | Extract tool use details from Claude Code sessions. |

### Authoring and integration

| Command | Description |
|---|---|
| [`cmsg`](cmd/cmsg) | Format stdin as Claude messages in NDJSON format. |
| [`mksid`](cmd/mksid) | Generate timestamp-sorted session IDs with git repo context. |
| [`cchandoff`](cmd/cchandoff) | Build a cross-tool handoff prompt from a prior session. |
| [`ccimport`](cmd/ccimport) | Convert Claude Code session JSONL into Gemini CLI chat format. |
| [`ccmemory`](cmd/ccmemory) | List and read Claude Code auto-memory files. |
| [`ccconfig`](cmd/ccconfig) | Manage Claude Code configuration. |

### Multi-agent coordination

| Command | Description |
|---|---|
| [`ccteam`](cmd/ccteam) | Manage Claude Code agent teams. |
| [`ccagent`](cmd/ccagent) | Inspect agent status and coordinate agents. |
| [`ccspawn`](cmd/ccspawn) | Spawn Claude Code agent processes in teammate mode. |
| [`ccinbox`](cmd/ccinbox) | Read and write agent inbox messages. |
| [`cctask`](cmd/cctask) | Manage persistent tasks for agent teams. |
| [`ccapprove`](cmd/ccapprove) | Handle plan and permission approval workflows. |

### Meta

| Command | Description |
|---|---|
| [`cctl`](cmd/cctl) | Unified control command dispatching to the others (`cctl m` ≡ `cmsg`, `cctl r` ≡ `creplay`, etc.). |

Run any binary with `-h` for full flag documentation, or read its
[godoc](https://pkg.go.dev/github.com/tmc/cc) page.

## Editor integration

A small Vim plugin under [`vim/`](vim/) provides syntax highlighting
and filetype detection for Claude Code session JSONL files.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE).
