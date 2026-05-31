# cc

[![Go Reference](https://pkg.go.dev/badge/github.com/tmc/cc.svg)](https://pkg.go.dev/github.com/tmc/cc)

A Go toolkit for working with Claude Code and OpenAI Codex CLI session
data. `cc` provides a small library for parsing session JSONL plus a
collection of CLI tools for generating session IDs, formatting messages,
replaying, searching, and indexing sessions across both agents.

## Quickstart

The core tools compose as a Unix pipeline:

```sh
# Create a session, format a message, replay it later.
export SID=$(mksid)
echo "user input" | cmsg | tee session-$SID.ndjson
creplay $SID
```

## Install

```
go install github.com/tmc/cc/cmd/...@latest
```

This installs every binary in `cmd/` (currently 27). To install just one:

```
go install github.com/tmc/cc/cmd/ccfmt@latest
```

## Streaming and NDJSON

The tools read and write NDJSON on stdin/stdout, one JSON object per
line. They are designed for Unix pipes: no input buffering, no batching,
output flushed per line. `cmsg`, `ccfmt`, `cccat`, and friends all
operate as filters and compose with `tee`, `grep`, `jq`, and `xargs`.

## Session IDs

`mksid` generates timestamp-sortable UUIDs that embed a hash of the
current git repo. IDs sort chronologically by creation time, and the
embedded repo hash correlates sessions back to a working tree without
extra lookups. Use them as filenames, log keys, or correlation IDs
across `cc` tools.

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
| [`ccsessions`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccsessions) | List and summarize coding-agent sessions. |
| [`ccfmt`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccfmt) | Format coding session transcripts (text or markdown). |
| [`creplay`](https://pkg.go.dev/github.com/tmc/cc/cmd/creplay) | Replay a coding-agent session in a terminal UI. |
| [`cccat`](https://pkg.go.dev/github.com/tmc/cc/cmd/cccat) | Filter and display coding-agent session entries. |
| [`cchistory`](https://pkg.go.dev/github.com/tmc/cc/cmd/cchistory) | Search through Claude Code session history. |
| [`cctime`](https://pkg.go.dev/github.com/tmc/cc/cmd/cctime) | Show a timeline of events in a coding-agent session. |

### Search, resume, indexing

| Command | Description |
|---|---|
| [`cass`](https://pkg.go.dev/github.com/tmc/cc/cmd/cass) | Index and search AI coding-agent session history (Claude Code, Codex, etc.). |
| [`ccresume`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccresume) | Find a recent coding-agent session and resume it. |
| [`ccstats`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccstats) | Report token usage, tool counts, and timing for sessions. |
| [`cass requests`](https://pkg.go.dev/github.com/tmc/cc/cmd/cass) | Show indexed API request breakdowns and capture metadata. |
| [`ccloc`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccloc) | Print the Claude Code agent cache location for a directory. |

### Inspection and analysis

| Command | Description |
|---|---|
| [`ccdiff`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccdiff) | Show file changes made during coding-agent sessions. |
| [`ccfiles`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccfiles) | Extract file operations from coding-agent sessions. |
| [`ccerr`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccerr) | Find errors, failures, and retries in coding-agent sessions. |
| [`cctool`](https://pkg.go.dev/github.com/tmc/cc/cmd/cctool) | Extract tool use details from coding-agent sessions. |

### Authoring and integration

| Command | Description |
|---|---|
| [`cmsg`](https://pkg.go.dev/github.com/tmc/cc/cmd/cmsg) | Format stdin as Claude messages in NDJSON format. |
| [`mksid`](https://pkg.go.dev/github.com/tmc/cc/cmd/mksid) | Generate timestamp-sorted session IDs with git repo context. |
| [`cchandoff`](https://pkg.go.dev/github.com/tmc/cc/cmd/cchandoff) | Build a cross-tool handoff prompt from a prior session. |
| [`ccimport`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccimport) | Convert Claude Code session JSONL into Gemini CLI chat format. |
| [`ccmemory`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccmemory) | List and read Claude Code auto-memory files. |
| [`ccconfig`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccconfig) | Manage Claude Code configuration. |

### Multi-agent coordination

| Command | Description |
|---|---|
| [`ccteam`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccteam) | Manage Claude Code agent teams. |
| [`ccagent`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccagent) | Inspect agent status and coordinate agents. |
| [`ccspawn`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccspawn) | Spawn Claude Code agent processes in teammate mode. |
| [`ccinbox`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccinbox) | Read and write agent inbox messages. |
| [`cctask`](https://pkg.go.dev/github.com/tmc/cc/cmd/cctask) | Manage persistent tasks for agent teams. |
| [`ccapprove`](https://pkg.go.dev/github.com/tmc/cc/cmd/ccapprove) | Handle plan and permission approval workflows. |

### Meta

| Command | Description |
|---|---|
| [`cctl`](https://pkg.go.dev/github.com/tmc/cc/cmd/cctl) | Unified control command dispatching to the others (`cctl m` ≡ `cmsg`, `cctl r` ≡ `creplay`, etc.). |

Run any binary with `-h` for full flag documentation, or read its
[godoc](https://pkg.go.dev/github.com/tmc/cc) page.

## Editor integration

A small Vim plugin under [`vim/`](vim/) provides syntax highlighting
and filetype detection for Claude Code session JSONL files.

## See also

- [`cass/DATAMODEL.md`](cass/DATAMODEL.md) — formal data model for the
  session index (node and edge types).
- [`docs/TRACE_INTEROP.md`](docs/TRACE_INTEROP.md) — trace interop
  notes for cross-agent session data.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE).
