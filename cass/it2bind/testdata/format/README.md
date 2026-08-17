# Binding event format: conformance fixtures

Every `.ndjson` file here is one case in the on-disk format that a coding
agent's `SessionStart` hook appends to

    ~/.it2/sessions/<it2_sid>/artifacts/claude-code-events.ndjson

and that any reader of that store must agree about. The format is a **file
format, not an API**: more than one implementation reads it, in more than one
repository, and they are expected to stay independent. What keeps them from
drifting is this corpus, not a shared import.

Known readers as of 2026-08-17:

- `github.com/tmc/cc/cass/it2bind` (this package) → `ccresume`
- `github.com/tmc/it2` `internal/agentbind` → `it2 session find --conversation`,
  `it2 session send-text --require conversation=<id>`

`agentbind` is under `internal/`, so it cannot be imported outside `it2`, and
`cc` does not depend on `it2`. That is deliberate: coupling two repositories'
release cycles over a ten-line JSON shape costs more than it saves. These
fixtures are the substitute. A second implementation should copy this directory
and assert the same outcomes.

## The expected outcome of each case

| File | Must parse? | Why |
|---|---|---|
| `binding.ndjson` | yes | The ordinary record. Every field present. |
| `prefixed.ndjson` | yes | `_it2.it2_sid` carries the `wNtNpN:` pane-position prefix. A reader must canonicalize to the bare UUID; the prefix is a location and goes stale when a pane moves, so keying on it splits one conversation across directories. |
| `lowercase.ndjson` | yes | UUID in lower case. Canonicalization must not change case — two spellings of one id must not become two ids. |
| `unbound.ndjson` | **no** | No `_it2`. This is what a writer predating the enrichment produces; the records are real but unusable, and a reader must drop them rather than treat them as bindings. |
| `wrapped.ndjson` | **no** | The `{raw, _it2}` wrapper the writer emits for a non-object hook payload. Has an it2 session id but no `session_id`, so it cannot be grouped. |
| `malformed.ndjson` | **no** | A blank line and an invalid-JSON line. Both are skipped without failing the file: one bad record must not discard the good ones around it. |

The distinction that matters most is `unbound` vs `malformed`. Both yield zero
usable records, and they have different causes — a stale writer versus a
corrupt line — so a reader that reports only "no bindings" cannot tell a
redeploy from a data-repair job. See `cchooks store` in this repo for a
diagnostic that separates them.

## Normative field names

These names are the format. They are listed because the drift they prevent is
not hypothetical: on 2026-08-17 `it2`'s `internal/agentbind` read
`_it2.session_id` and `_it2.timestamp` while the writer emitted `it2_sid` and
`ts`, so its `At` silently fell back to file mtime — "when this file was last
touched" substituted for "when this binding happened."

    session_id        string  required, top level
    transcript_path   string  optional, top level
    cwd               string  optional, top level
    hook_event_name   string  optional, top level
    _it2.it2_sid      string  required
    _it2.ts           string  required, "2006-01-02T15:04:05.000Z"
    _it2.boot_id      string  optional

`_it2.ts` carries **milliseconds**. A reader parsing a layout without them
rejects every real record and falls back to whatever its error path does. The
fixtures here stamp milliseconds for exactly this reason: a corpus whose values
do not match what the writer emits cannot police the drift it exists to catch.

## Field notes

- `session_id` is the agent's own session id (the transcript filename stem).
  Required for grouping.
- `_it2.it2_sid` is the iTerm2 pane id. Required, canonicalized on read.
- `_it2.boot_id` is `kern.bootsessionuuid`. Metadata only: iTerm2 restores a
  pane across a reboot keeping its session id, so a boot change is **not** a
  binding boundary.
- `_it2.ts` is the writer's ISO-8601 stamp, used for interval ordering.
- `source` distinguishes `startup` from `resume`. A session resumed into a
  different pane is a new binding, which is why the mapping is one-to-many
  over time rather than a key.

Each record must be written with a **single** `write(2)` on an `O_APPEND`
descriptor, newline included. A writer that emits the line and the newline
separately interleaves under concurrency and tears records.
