# Binding event format: conformance fixtures

Each file here is one case in the on-disk format that a coding agent's
`SessionStart` hook appends to

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
| `legacy-pretty.jsonl` | **no** | The older store, which is not this format at all. See below — the extension lies. |

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

## The older store is not this format, and its name says otherwise

Before this format there was `~/.it2/sessions/<it2_sid>/claude-code-hook-events.jsonl`.
Despite the extension it is **not line-delimited**: the writer pretty-printed
each payload, so one record spans about seven lines. `legacy-pretty.jsonl` is
that shape. Measured on one machine on 2026-08-17: 140,670 lines across 54
files, and **zero** of those lines is a single-line JSON object carrying
`session_id`. A line-oriented reader recovers nothing from any of them. None of
the records carry `_it2` either — they predate the enrichment, so even when
decoded they are `unbound`.

The trap is not that the data is unusable. It is that a reader can *list* the
path and appear to handle it. `it2`'s `agentbind` carried this file as a second
entry in its `eventFiles` fallback list; the fallback could not match a single
record, and reported that as `nil, nil` — indistinguishable from "this pane has
no stream." The code read as legacy support and delivered none. The entry has
since been removed, with a comment at `eventFiles` saying the omission is
deliberate, because an absent path invites re-adding.

**A streaming decoder is not the obvious fix, and the obvious fix is wrong.**
`agentbind`'s reader seeks to the last 64 KiB and walks backward, because its
contract is "the last binding wins" and these files grow without limit. A
`json.Decoder` handed that window begins mid-object, errors on the first token,
and does not resync — Go's decoder poisons the stream on the first error. So
swapping in a decoder while keeping the tail seek reads nothing at all. Whole
files fare better but not cleanly: a strict decode recovers 5,928 objects and a
resyncing one 6,767, meaning **839 objects across 3 files are lost to
mid-object interleaving** — two concurrent appends to a pretty-printed store
tear the same way line writers do, which is why the single-`write(2)` rule
above exists. Strict decoding stops at the first tear, so under a
last-binding-wins contract it returns a *stale* binding rather than none: the
quiet-wrong answer, which is worse than the empty one.

So, normatively: a reader either decodes whole files with a **resyncing** pass
over concatenated values, accepting that this abandons any tail bound, or it
does not name the path at all. Listing a path you cannot parse converts a
missing feature into a silent one.

## Reporting emptiness: say which stage yielded zero

Every failure in this format presents identically — no bindings — and the
causes need different fixes, so "empty" is not a usable diagnosis. A reader
that reports emptiness must say which stage first yielded zero:

- `store-unwritten` — no handler is registered. Nothing has ever run.
- `store-empty` — a handler is registered and runs, and writes nothing.
- `store-unbound` — records exist but carry no `_it2`; a stale writer.
- `store-unresolved` — records are bound but cannot be grouped into panes.

Only the first is fixed by redeploying a handler. Collapsing the four into one
message is what let this store sit unwritten while every check passed. See
`cchooks store` in this repo.

## A warning about this corpus

A conformance corpus is an oracle, and an oracle built from the same wrong
assumption as the code certifies the bug instead of catching it. This has
happened four times here, all on 2026-08-17, all inside the work that was
supposed to prevent it:

1. `agentbind`'s own `TestLatestReadsTheLastEvent` used `_it2.session_id` and
   `_it2.timestamp` in its fixture — the reader's wrong names. The assertion
   was correct; the fixture was generated from the defect, so the test passed.
2. These fixtures first stamped `_it2.ts` without milliseconds, which the
   writer always emits. The corpus would have **passed a broken reader and
   failed a correct one**.
3. `agentbind`'s `TestPanes` passed by luck: both bindings fell back to file
   mtime, and the mtimes happened to order the same way as the assertion.
4. `agentbind`'s `TestLatestFallsBackToTheOlderFilename` asserted the legacy
   fallback worked, using the fixture `{"session_id":"LEGACY"}` — one compact
   line. The writer has never produced such a line; all 140,670 are fragments
   of pretty-printed objects. The test did not merely miss the dead path, it
   **certified** it, which is why the path survived for months: it had
   coverage, and the coverage was the reason nobody removed it.

The pattern is one thing: a check whose inputs were derived from the
implementation rather than from the writer. When adding a case here, take the
values from what the writer actually emits — read a real record — and never
from what a reader expects. Field names alone would not have caught (2); the
key was right and the value layout was wrong. That is why the section above is
normative about **formats**, not just names.

Case (4) is the one to remember, because its symptom is different. In (1)–(3)
the oracle agreed with a bug in code that did something; the value was wrong.
In (4) the oracle kept alive code that did **nothing at all** — a phantom
capability, green in CI, backed by a fixture no writer could produce. That
failure mode is invisible to every review that asks "is this tested?" and
visible only to one that asks "where did this fixture's bytes come from?" An
invented fixture cannot fail, so it certifies whatever it is pointed at.
The replacement, `TestLatestIgnoresTheOlderFilename`, feeds a realistically
pretty-printed record and asserts `Latest` returns nil — pinning the decision
rather than deleting the coverage, so the omission stays deliberate.
