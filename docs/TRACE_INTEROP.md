# tmc/cc — Agentic Trace Interop Design

**Status:** draft 0.4
**Date:** 2026-04-19
**Previous:** 0.1, 0.2, 0.3 (all reviewed). 0.4 folds in 0.3 review.
**Context:** tmc/cc is a collection of ~26 Go CLIs for analyzing coding-agent session files (Claude Code, Codex, Gemini CLI, Cursor, Windsurf/OpenClaw). Current storage: vendor-native JSONL on disk + optional SQLite FTS5 index via `cass`.

---

## Changes from 0.3

- **Event content is now structured,** not JSON-stringified. Per OTel GenAI spec: events MUST record content in structured form. Content lives on event *attributes* (`gen_ai.user.message` attribute on the `gen_ai.user.message` event), not in any "body" field (events have no body; that's a LogRecord concept).
- **Fallback when structured attributes unsupported:** if the OTel Go SDK/exporter doesn't support nested maps on event attributes, content falls back to a JSON-encoded string on a *span attribute* (as the OTel spec permits for spans — MAY, not MUST), and we warn on stderr. We do not violate the event MUST requirement.
- **Trace/span ID byte-slicing explicit.** `trace_id = sha256.Sum256(sessionId)[0:16]` (operating on the `[32]byte` array → 16 raw bytes → 32 hex chars when rendered). Same for span_id at `[0:8]` → 8 raw bytes → 16 hex chars. Reviewers have fallen into the "slice the hex string" trap before; don't.
- **`-dialect openinference`** now also:
  - Swaps `schema_url` to `https://arize.com/schemas/openinference/1.0.0`.
  - Injects required `openinference.span.kind` attribute (LLM, AGENT, TOOL, RETRIEVER) on every span.
  - Attribute-mapper is not just a key swap — it's an attribute rewrite pipeline with span-kind inference.
- **Subagent summary text bubbled up.** When a subagent (`isSidechain=true`) emits its final `tool_result` back to the parent's `tool_use`, that content is also emitted as a `gen_ai.assistant.message` event on the parent's *turn span*, not only under the subagent subtree. The parent's waterfall must show what the parent actually saw.
- **Added `cc.agent.isolation_mode`** attribute on subagent root spans (values: `worktree`, `remote`, `in_process`, `unknown`) so correlations between subagent isolation and failure rates are queryable.
- **Added `gen_ai.client.token.usage` metric emission.** Explicit counter/histogram, not derived by collectors — many backends expect the direct stream.
- **Dropped "event body" nomenclature** throughout. Events have `name` + `attributes` only.

---

## Design principles

1. **Authoritative data is vendor JSONL on disk.** Claude Code, Codex, Gemini produce richer traces than any current standard — fork/rewind, compaction boundaries, `isSidechain`, harness-injected directives. We project onto standards for export; we do not re-ingest.

2. **Export-first. Never ingest.** Every standard here is a *view* of the truth on disk.

3. **Lossy export is acceptable; silent loss is not.** When an export format can't represent a concept, log a one-line stderr warning naming the dropped field.

4. **No new storage format.** Vendor JSONL is source of truth; SQLite FTS5 is the only derived index.

5. **Idempotent output.** Re-running an export on the same input produces identical bytes. Critical for caching, dedup, and diffing.

6. **Academic frameworks inform design, not dependencies.** VCC and AgentTrace (AAAI 2026) provide mental models; we steal ideas, not code.

---

## What to export, ranked

### Tier 1 — build now

#### 1. OTel GenAI semantic conventions (OTLP-JSON) — `ccotel`

**Why:** Winning format. AAIF/Linux Foundation hold the ecosystem. OTel is actively absorbing OpenInference. Datadog, Dynatrace, Arize Phoenix, Langfuse, Braintrust all consume it.

**What:** Reads vendor JSONL on stdin or files; emits OTLP-JSON spans. Targets OTel semconv 1.40.0+.

**Trace/span ID strategy (deterministic):**
```go
trace := sha256.Sum256([]byte(sessionId))     // [32]byte
traceID := trace[0:16]                         // 16 raw bytes → 32 hex chars
span := sha256.Sum256([]byte(sessionId+"|"+entry.UUID+"|"+kind))
spanID := span[0:8]                            // 8 raw bytes → 16 hex chars
```
Slice the **byte array**, not the hex string. Reruns are idempotent; collectors dedupe naturally.

**Resource attributes:**
```
service.name           = (inferred: claude-code | codex | gemini-cli; fallback: cc)
service.version        = (agent version if known)
schema_url             = https://cc.tmc.dev/schemas/0.1.0
```

**Span mapping:**

| cc concept | OTel representation |
|---|---|
| Session | Root span, kind=CLIENT. `gen_ai.conversation.id = sessionId` |
| Assistant turn | Child span. `gen_ai.operation.name = chat`, `gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons` |
| User message | Span event on turn span, event name `gen_ai.user.message`, structured attributes on the event (role, content parts) |
| System / CLAUDE.md / AGENTS.md resolved rules | `gen_ai.system_instructions` on root span; also one `gen_ai.system.message` event with structured content attributes |
| Assistant message content | Span event on turn span, event name `gen_ai.assistant.message`, structured content attributes |
| `tool_use` block | Child span of turn. `gen_ai.operation.name = execute_tool`, `gen_ai.tool.name`, `gen_ai.tool.call.id`, `gen_ai.tool.call.arguments` |
| `tool_result` block | Span event on tool span, event name `gen_ai.tool.message`, structured attributes carry content. Final-result summary set as `gen_ai.tool.call.result` attribute on the tool span itself. |
| Subagent (`isSidechain=true`) | Child span tree under the spawning `tool_use`. Root span of subtree: `gen_ai.operation.name = invoke_agent`, `gen_ai.agent.name = Task`, `cc.agent.id`, `cc.agent.isolation_mode`. Subagent final result is also emitted as a `gen_ai.assistant.message` event on the **parent's turn span** so the parent waterfall shows what was returned. |
| MCP tool call | On the `execute_tool` span: `mcp.method.name`, `mcp.protocol.version`, `mcp.resource.uri` (when applicable), `mcp.session.id` |
| Fork | Span link on new branch's first span: `cc.branch.kind = fork`, `cc.branch.source.span_id = <origin>` |
| Rewind | Span link: `cc.branch.kind = rewind`, `cc.branch.target.span_id = <rewound-to>` |
| Compaction (one event per stage) | Span events on turn span: `cc.compaction.budget_reduction`, `cc.compaction.snip`, `cc.compaction.microcompact`, `cc.compaction.context_collapse`, `cc.compaction.auto_compact`. Attributes per event: `cc.tokens_freed`, `cc.tokens_before`, `cc.tokens_after` (and `cc.snip_tokens_freed` on the snip event) |
| iTerm2 send-text | Span event on tool span: `cc.it2.send_text` with `cc.it2.src`, `cc.it2.dst` |
| Plan / autonomous / bypass mode | `cc.mode = plan\|autonomous\|bypass_permissions` on turn span |
| Permission hook events | Span events under turn span: `cc.hook.<event_name>` (27 hook events per Claude Agent SDK) |

**Metrics emitted:**
```
gen_ai.client.token.usage    histogram  {attributes: gen_ai.request.model, gen_ai.token.type=input|output}
cc.compaction.tokens_freed   histogram  {attribute: cc.compaction.stage}
cc.tool.calls                counter    {attribute: gen_ai.tool.name}
```
The GenAI standard histogram is emitted directly rather than relying on collectors to derive it from span attributes (many backends require the explicit metric stream).

**Event content encoding:** structured. Event attributes carry the content (role, parts, tool_call_id, etc.) per the OTel GenAI spec MUST. If a specific exporter or SDK version doesn't support nested maps on event attributes, fall back to a JSON-encoded string on the *span attribute* (spec allows this for spans as MAY) and emit a one-line stderr warning. Events themselves never carry a JSON-string payload; events do not have a "body" field.

**`-dialect openinference` semantics:** not just a key swap.
- Sets `schema_url = https://arize.com/schemas/openinference/1.0.0` on Resource + Scope.
- Injects `openinference.span.kind` on every span (LLM, AGENT, TOOL, RETRIEVER) — required by OpenInference consumers.
- Rewrites OTel GenAI attribute keys to OpenInference equivalents (`gen_ai.*` → `llm.*` where applicable).

**Flag surface:**
```
ccotel [flags] [file...]
  -format otlp-json         OTLP/JSON (default)
  -format otlp-proto        OTLP/protobuf
  -dialect otel             Pure OTel GenAI (default). schema_url = cc.tmc.dev/schemas/0.1.0
  -dialect openinference    Swaps attribute mapper + schema_url to OpenInference 1.0.0. Injects openinference.span.kind on every span.
  -service NAME             Override service.name (default: inferred)
  -include-content          Include content in span events (default: true)
  -redact PATTERN           Regex to redact from content; repeatable
  -resource KEY=VALUE       Additional resource attributes; repeatable
```

**Estimated size:** 500–600 LOC. One main file, a span-builder helper, a deterministic ID helper. Golden-file tests against known JSONL fixtures.

**Open questions:**
- Emit to stdout only, or support `-otlp-endpoint URL`? Start stdout-only. Defer gRPC concerns to `otelcol`.
- Multiple models per session (classifier Haiku + main Opus): emit sibling spans. Confirmed — OTel requires one `gen_ai.request.model` per span.

#### 2. AGENTS.md / SKILLS.md — `ccrules`

**Why:** De facto standard. VS Code, Copilot, Cursor, Windsurf, Gemini CLI, Zed all consume it. Cheap to parse. High signal when correlated with traces. **Must ship before `ccotel`** because `ccotel` depends on it.

**Surface:**
```
ccrules                     # list AGENTS.md hierarchy for cwd
ccrules -show               # print resolved agent rules (root + subdir merge)
ccrules -skills             # list available SKILLS.md
ccrules -json               # machine-readable output (used by ccotel)
ccrules -project DIR        # target a different project
```

**Output (`-json`):**
```json
{
  "files": ["/path/AGENTS.md", "/path/subdir/AGENTS.md"],
  "resolved": "...full merged content...",
  "skills": [{"name": "...", "path": "...", "level": "L1|L2|L3"}]
}
```

**Estimated size:** 200 LOC. `-violations` deferred.

#### 3. Agent Trace (Cursor spec) — `ccattest`

**Why:** cc's primary domain is coding-agent sessions. Agent Trace is the only standard for line-range attribution of AI-generated code. Draft spec, ~3 months old, backed by Cursor. A VCS-level attribution standard is inevitable and this is the live contender.

**Surface:**
```
ccattest [flags] [file...]
  -output FILE              Output path (default: stdout)
  -model MODEL              Override emitted model identifier
  -agent TOOL               Override emitted tool name (default: inferred)
  -since DURATION           Only attest edits after this cutoff
  -file-filter GLOB         Only attest matching files; repeatable
  -granularity file|lines   Line-ranges (default: file) or whole-file
```

**Mapping:**
- Each `Edit`/`Write` tool_use → one contribution on the target file.
- `contributor.type = ai`, `contributor.model = anthropic/claude-opus-4-7-20260401` (models.dev format).
- `ranges` from `old_string`/`new_string` diff for Edit; whole-file for Write.
- One Agent Trace record per session. Emitted `version: "0.1.0"` pin; bump explicitly.

**Estimated size:**
- File-granularity: ~250 LOC
- Line-granularity: +**1.5 weeks** of work. Whitespace/AST edge cases in mapping string replacements to 1-indexed line ranges are the real cost.

### Tier 2 — speculative

#### 4. OWASP AOS / OCSF

Security-audit framing. Wrong tool for dev-time analysis. Users can pipe `ccotel` output through a SIEM shim.

**Recommendation:** do not build in-tree.

---

## What NOT to support

- **Ingesting any of these formats.**
- **A "cc trace format."** Vendor JSONL is the format.
- **Direct OTel collector push over gRPC.** Pipe-friendly only.
- **Live streaming OTel while a session is running.** Agent harness's job.
- **`ccfmt -format otel-json`.** Users pipe through `ccotel`.
- **`cc.v1.mcp.*` attributes.** OTel has official MCP semconv. Use it.

---

## Impact on existing commands

| Command | Change |
|---|---|
| `ccfmt` | No change. |
| `ccstats` | No change. |
| `cass` | `cass index` could optionally emit OTel spans via `ccotel` on indexed sessions, behind a flag. |
| `cctl` | Register `ot` → `ccotel`, `at` → `ccattest`, `rules` → `ccrules`. |
| `cchandoff` | No change. |
| existing `ccagent` | No change. Naming collision avoided. |

---

## Custom-attribute namespace policy

All cc-specific attributes are prefixed `cc.*` (no version number in the key).

- **Schema URL** is the source of truth: `https://cc.tmc.dev/schemas/0.1.0`. Bump on breaking changes. Emit on OTel Resource + Scope.
- **Breaking changes** — new attribute name, new `schema_url`. Emit `OTel schema transformations` in a migration file; let downstream schema processors auto-migrate.
- If OTel GenAI later standardizes an equivalent, we emit *both* during transition, deprecate `cc.*` with a stderr warning, and drop the custom attribute in the next schema version.
- Every `cc.*` attribute is documented in `cmd/ccotel/ATTRIBUTES.md`.

---

## Open questions for reviewers

1. `ccotel -dialect openinference` — one binary vs separate `ccoinf` binary? Lean: one binary, two dialects (swap maps only; transport identical).
2. Fork/rewind: separate traces + span links, not one trace spanning the whole forked session tree. Reviewer confirmed this; parking as decided.
3. Default `service.name`: inferred per collector; fallback `cc`. Parking as decided.
4. Should `ccrules -json` be versioned in its output schema, or shift as needed? Lean: versioned (`"schema": "cc-rules/0.1"`) because downstream tools parse it.

---

## Non-goals

- Agentic-benchmark harness (MLCommons AILuminate).
- Live observability backend.
- Proprietary SaaS trace APIs (LangSmith, AgentOps, Braintrust). They all accept OTel.
- Replacing anyone's JSONL format.

---

## Rollout (reordered)

1. **PR 1: `ccrules`** — list + show + -json. ~3 days. No dependencies.
2. **PR 2: `ccotel`** — OTel GenAI (pure) export. Deterministic IDs. `ccrules -json` integration for `gen_ai.system_instructions`. Metrics histogram. Golden-file tests. ~1.5 weeks.
3. **PR 3: `ccattest` file-granularity** — Agent Trace emission. ~3 days.
4. **PR 4: `ccotel -dialect openinference`** — attribute-mapper swap. ~2 days.
5. **PR 5: `ccattest -granularity lines`** — diff-based line ranges. ~1.5 weeks. (Re-estimate before starting.)
6. **Defer:** `ccrules -violations`, OCSF/AOS, `-otlp-endpoint`, OTel Collector receiver plugin.

Total initial surface: 3 new commands, ~1100 LOC, no changes to existing commands. Fully additive.

---

*End of draft 0.4.*
