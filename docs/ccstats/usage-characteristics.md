# ccstats: Usage-Characteristics Report

Status: First slice shipped in `cmd/ccstats` (M1); remaining items stay draft.
Author: tmc
Date: 2026-04-20
Scope: Add a second report tier to `ccstats` (or a sibling `ccanalyze` if we decide to split) that mirrors the cross-session "usage characteristics" view Claude Code and other session providers surface in their consumer UIs.

## 1. Motivation

Today `ccstats` reports per-session facts: tokens, tools, messages, duration. Useful, but the question "*how* am I using this assistant?" — which drives whether the user should change behavior — needs **cross-session, time-windowed, independent characteristics**. This is the report Anthropic shows in the Claude Code "Usage" panel:

```
Last 24h · these are independent characteristics of your usage, not a breakdown

  86% of your usage was while 4+ sessions ran in parallel
  51% of your usage was at >150k context
  50% of your usage came from subagent-heavy sessions
  46% of your usage came from sessions active for 8+ hours
```

Key property of this view (from the UI's own disclaimer): **the percentages do not have to sum to 100.** Each line answers a different yes/no question about a unit of usage. A single request can count toward all four characteristics, or none.

Our goal: compute the same thing locally from `~/.claude/projects/**/*.jsonl`, plus a few additional characteristics the hosted UI doesn't show.

## 2. Goals

1. Report **independent** usage characteristics (not a breakdown) over a time window.
2. Drive the percentages off a **cost-weighted** unit of usage (tokens-billed, not request count or wall-clock time), matching the hosted UI's notion.
3. Surface **actionable** characteristics — each line should map to a concrete behavior change (compact, clear, queue, pick cheaper model).
4. Stay local. No network. Read-only over `~/.claude/projects/**/*.jsonl`.
5. Fit into existing `ccstats` ergonomics (`-since 24h`, piped file lists, `-format json`).

## 3. Non-goals

- Replacing the per-session report (still the default).
- Predictive modeling or projecting forward spend.
- Replicating Anthropic's cost curve exactly — we use observed tokens × published rates.
- Cross-user comparisons.

## 4. Terminology

| Term | Definition |
|---|---|
| **Request** | One assistant turn (`role:"assistant"` entry with `usage`) |
| **Unit of usage** | The weight we attribute to each request — see §6 |
| **Window** | The time slice we're reporting over (24h, 7d, 30d, custom) |
| **Characteristic** | A yes/no predicate evaluated *per request* (or per session, projected onto its requests) |
| **Session** | One `.jsonl` file, keyed by `sessionId` |
| **Parallel** | Two sessions are parallel at instant *t* if both have a request whose window `[start, start+turnDuration]` contains *t* |

## 5. The characteristics

### 5.1 Matching the hosted UI

These are the four Claude Code shows today. We replicate all of them:

1. **`parallel4+`** — "% of usage while 4+ sessions ran in parallel"
   *Predicate:* at the moment this request was issued, ≥4 distinct sessions had a request within the last *N* minutes (default 2).
   *Callback:* "If you don't need them all at once, queueing uses the limit more evenly."

2. **`context150k+`** — "% of usage at >150k context"
   *Predicate:* request had `input_tokens + cache_read_input_tokens + cache_creation_input_tokens ≥ 150,000`.
   *Callback:* "Longer sessions are more expensive even when cached. `/compact` mid-task, `/clear` when switching."

3. **`subagentHeavy`** — "% of usage from subagent-heavy sessions"
   *Predicate:* request's session had ≥30% of its requests flagged `isSidechain=true` over the window.
   *Callback:* "Each subagent runs its own requests. Be deliberate — configure cheaper models for simpler subagents."

4. **`longRunning8h+`** — "% of usage from sessions active for 8+ hours"
   *Predicate:* request's session spans ≥8h from first to last timestamp within the window.
   *Callback:* "These are often background/loop sessions. Make sure it's intentional."

### 5.2 Additional local-only characteristics

Because we have the raw `.jsonl`, we can compute things the hosted UI can't or doesn't:

5. **`cacheMissHeavy`** — "% at <40% cache read ratio"
   Low `cache_read / (input + cache_read + cache_creation)` means we're paying to re-read context that a smarter prompt structure or fewer `/clear`s would have cached.
   *Callback:* "Keep prompts stable across turns; avoid unnecessary `/clear`."

6. **`compactFollow`** — "% of requests within 3 turns after a `/compact`"
   Compaction is expensive and triggers a full re-cache. If a lot of usage sits in that post-compact window, the user is compacting too eagerly.
   *Callback:* "Compact less often; `/clear` is cheaper when switching tasks."

7. **`outputHeavy`** — "% where output_tokens > 50% of input_tokens (cache-adjusted)"
   Long-form generation; specific cost shape worth calling out independently from context-size.
   *Callback:* "Consider if chunked requests or a cheaper output-heavy model would suit."

8. **`toolSpamHeavy`** — "% in sessions averaging >6 tool-uses per assistant turn"
   *Callback:* "High tool counts per turn usually mean under-planned tasks. Consider `TaskCreate` to scope work."

9. **`backgroundLoopShare`** — "% in sessions that look like loops"
   Heuristic: session has ≥N timer-like `ScheduleWakeup` or `CronCreate` tool uses, OR title matches `^/loop\b`.
   *Callback:* "Scheduled/looping sessions accumulate silently. Set a sunset."

10. **`model distribution`** — not a percentage line; a bar of `opus / sonnet / haiku` share of the unit of usage.
    Not framed as a "characteristic" line because it's a breakdown, not an independent yes/no.

We ship 5.1 by default; 5.2 behind `-verbose` to avoid firehose by default.

## 6. Choosing the unit of usage

The hosted UI's disclaimer — "these are independent characteristics of your usage" — only makes sense if *usage* has a defined denominator. Candidates:

| Unit | Pros | Cons |
|---|---|---|
| **Request count** | Trivial | 1 haiku request ≈ 1 opus request — misleading |
| **Wall-clock seconds** | Matches "activity" intuition | Distorted by streaming / think time |
| **Raw tokens** | Concrete | Cache-read tokens are much cheaper than input; over-weights long-cached sessions |
| **Cost-weighted tokens** (chosen default) | Lines up with the budget the user actually sees | Requires a rate table |
| **Billed tokens** | Closest to the invoice | Not locally observable for hosted Claude Code |

**Decision:** default to **cost-weighted tokens** using a small embedded rate table per model. The weight for one request is:

```
weight = input * rate_in
       + output * rate_out
       + cache_read * rate_cache_read
       + cache_create * rate_cache_create
```

`rate_*` values live in a small `pricing.go` table keyed by the assistant message's `model` field (e.g. `claude-opus-4-7`). Unknown models fall back to `rate_in = rate_out = 1, cache_read = 0.1` as a reasonable default.

Expose `--unit=requests|tokens|cost` as an override so the user can check how stable their ranking is across weightings.

## 7. Data shape

### 7.1 Input

`.jsonl` files under `~/.claude/projects/**/*.jsonl`, already parsed by `cc.ReadFile`. Relevant fields per entry:

- `sessionId`
- `timestamp`
- `parentUuid` (for turn-graph reconstruction)
- `isSidechain` (the subagent marker — already surfaced in `session.go:21`)
- `message.model`
- `message.usage.{inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens}`
- `message.content[].type == "tool_use"` (for tool-spam characteristic)
- `type == "system"`, `subtype == "compact_boundary"` (for compact-follow characteristic)

### 7.2 Intermediate types

```go
// cmd/ccstats/analyze.go (new file)
package main

import "time"

// Request is a single assistant turn, flattened for windowed analysis.
type Request struct {
    SessionID   string
    Timestamp   time.Time
    Model       string
    IsSidechain bool
    // Raw token counts.
    In, Out, CacheRead, CacheCreate int
    // Derived.
    Weight       float64 // cost-weighted tokens
    ContextSize  int     // in + cache_read + cache_create
    ToolUseCount int     // tool_uses in the same assistant message
}

// SessionAgg is per-session aggregates projected onto each Request in that session.
type SessionAgg struct {
    SessionID       string
    FirstTS, LastTS time.Time
    RequestCount    int
    SidechainCount  int
    ToolUseCount    int
    IsLoopLike      bool
}

// ParallelIndex answers "how many sessions were active at t?"
// Sparse impl: sorted slice of (t, +1) / (t+window, -1) events.
type ParallelIndex struct { /* ... */ }
func (p *ParallelIndex) ActiveAt(t time.Time) int { /* ... */ }
```

### 7.3 Output

```
Last 24h · these are independent characteristics of your usage, not a breakdown

  86%  4+ sessions running in parallel     [parallel4+]
  51%  context > 150k                       [context150k+]
  50%  subagent-heavy sessions              [subagentHeavy]
  46%  sessions active for 8+ hours         [longRunning8h+]

  Unit: cost-weighted tokens · Window: 2026-04-19 13:08 → 2026-04-20 13:08
  Requests: 4,812 · Sessions: 37 · Total weight: 1.42M tok-eq

  d=day  w=week  m=month
```

With `-verbose`:

```
  22%  cache-miss heavy (<40% read ratio)   [cacheMissHeavy]
  14%  within 3 turns of a /compact          [compactFollow]
  18%  output > 50% of cache-adj input       [outputHeavy]
   9%  sessions averaging >6 tools/turn      [toolSpamHeavy]
  31%  background/loop-like sessions         [backgroundLoopShare]

  Model share (cost-weighted):
    opus     61%  ████████████░░░░░░░░
    sonnet   35%  ███████░░░░░░░░░░░░░
    haiku     4%  █░░░░░░░░░░░░░░░░░░░
```

JSON (`-format=json`) emits the same values as a flat object for piping into dashboards:

```json
{
  "window": {"start": "...", "end": "...", "label": "24h"},
  "unit": "cost",
  "totals": {"requests": 4812, "sessions": 37, "weight": 1420000},
  "characteristics": {
    "parallel4+":     {"pct": 0.86, "weight": 1221200, "description": "...", "callback": "..."},
    "context150k+":   {"pct": 0.51, "weight": 724200, ...},
    ...
  },
  "model_share": {"claude-opus-4-7": 0.61, "claude-sonnet-4-6": 0.35, "claude-haiku-4-5": 0.04}
}
```

## 8. Algorithm

```
1. Discover files.
   Reuse cc.FindSessionFiles(since, "").

2. Single pass per file → []Request, SessionAgg.
   Reuse cc.ReadFile. No file is read twice.

3. Merge: []Request sorted by timestamp (stable). SessionAgg keyed by sessionId.

4. Build ParallelIndex from all Request.Timestamps with a per-request
   "active window" ending at Request.Timestamp + 2min (tunable).

5. For each Request, evaluate each characteristic's predicate.
   Accumulate weight into a bucket per characteristic.

6. Render: each characteristic's pct = bucket_weight / total_weight.
```

Complexity: O(N log N) dominated by the sort (N = total requests in window). Memory: O(N) flat slice of Requests.

## 9. Edge cases

- **Clock skew between sessions.** Rare in practice (single machine). If we go cross-machine later, normalize to UTC and document that parallelism is wall-clock not logical.
- **Missing `usage` on assistant messages.** Skip the request for weight purposes but still count it toward characteristics that don't require tokens.
- **Truncated/compacted sessions.** Compact summaries have `isCompactSummary=true` — exclude from request counts but *include* the `compact_boundary` system event so `compactFollow` is computable.
- **Unknown model.** Fall back to `"unknown"` bucket + generic rate. Warn once at end of run.
- **Single-request window.** All percentages are 0% or 100% — document this rather than suppress.
- **Extremely small windows (<1h).** Parallelism becomes noisy. Auto-widen the active-window from 2min to 5min when `window < 2h`, or document the flag.

## 10. Command surface

Split verdict: **extend `ccstats` with a subcommand**, don't create `ccanalyze`.

```
ccstats                              # existing per-session report (default)
ccstats characteristics               # new: 5.1 only
ccstats characteristics -verbose      # + 5.2
ccstats characteristics -since 7d
ccstats characteristics -since 7d -format json
ccstats characteristics -unit=requests
ccstats characteristics -parallel-window=5m
ccstats characteristics -context-threshold=200k
ccstats characteristics -long-running-threshold=12h
```

Shortcut keystrokes in the TUI-equivalent (if we build one later): `d` = 24h, `w` = 7d, `m` = 30d, matching the hosted UI's footer.

Flags deliberately kept few and thresholds named (not positional) — `ccstats` users today are scriptable, not interactive.

## 11. Testing

- **Unit: predicate correctness.** Table-driven. Feed synthetic `Request` with known values, assert characteristic assignment.
- **Unit: ParallelIndex.** Small fixture, compare `ActiveAt` outputs against a naive O(N²) reference.
- **Golden: report format.** Fixture with a known set of sessions → golden `.txt` and `.json`. Update via `-update` flag.
- **Integration: live data.** `ccstats characteristics -since 24h` run against the user's own `.jsonl` — smoke test, values must be in `[0,1]`, `total_weight > 0`, JSON round-trips.
- **Property: denominators.** For any non-trivial window, each `characteristic.weight ≤ total_weight`. Percentages must be in `[0, 1]`. Sum is *not* bounded — that's the whole point.

## 12. Implementation plan

1. **M1** — Add `cmd/ccstats/analyze.go` + `pricing.go` + `parallel.go`. Implement 5.1 only. Ship.
2. **M2** — Add 5.2 characteristics behind `-verbose`.
3. **M3** — JSON schema + golden tests.
4. **M4** — Refactor shared parsing out of `cmd/ccstats/main.go` into an internal package if the file grows past ~500 LOC.
5. **M5** — (Optional) a small `ccstats watch` that refreshes the report every 60s, for users who want the Claude-Code-panel-shaped experience in their terminal.

## 13. Open questions

1. **Other providers.** Do we extend beyond Claude Code `.jsonl` to OpenAI Codex sessions, Gemini CLI, etc.? `cc.ReadFile` currently has a Codex adapter (`reader_codex_test.go`) — worth surveying what usage-metadata they expose before claiming "other session providers."
2. **Pricing table maintenance.** Do we embed rates in source or ship a small YAML/JSON in `~/.config/cc/pricing.yaml` that users can override? Lean: embed defaults, allow override file.
3. **Cohort comparison.** Should we ship a "vs. last week" delta line under each characteristic? Cheap to compute (two windows, same pipeline) and makes the report actionable instead of descriptive.
4. **Privacy.** These reports never leave the local machine — do we need a `-redact` mode that strips `sessionId` and file paths before JSON output? Lean: yes, for users who want to share the report.

## 14. Appendix: why percentages don't sum to 100

The header line in the hosted UI — "these are independent characteristics of your usage, not a breakdown" — is load-bearing. A single request at 180k context running in parallel with 5 other sessions in a 10-hour background loop counts toward **three** characteristics simultaneously. If we flattened this into a mutually-exclusive breakdown, users would lose exactly the insight they need: "which of these four levers is the one I should pull?"

Keep the UI disclaimer prominent. Print it above the list. Users who want a breakdown can use the existing per-session report.
