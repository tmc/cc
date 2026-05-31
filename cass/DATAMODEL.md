# CASS Data Model Specification

Formal specification of entities, relationships, and data flows in the
Claude Code usage tracking system. Derived from empirical analysis of
JSONL session files, Proxyman HAR captures, and Claude Code source code.

Version: 5 (2026-05-12)

## Notation

    G = (V, E) where V = ⋃ Vₙ for node types n, E = ⋃ Eᵣ for relations r

Confidence levels for edges:

    authoritative  — join on a stable, unique identifier
    derived        — computable from authoritative edges (transitive closure)
    heuristic      — probabilistic; based on timestamps, patterns, or conventions
    ephemeral      — valid only while a process is running

---

## 1. Filesystem Layout

Claude Code stores data across three directory trees:

    ~/.claude/
    ├── projects/                          # session data, per-project
    │   └── <encoded-path>/                # e.g. -Volumes-tmc-go-src-...
    │       ├── sessions-index.json        # session inventory (IndexEntry[])
    │       ├── <sessionId>.jsonl           # conversation log (Entry[])
    │       └── <sessionId>/
    │           └── subagents/
    │               ├── agent-<agentId>.jsonl      # subagent log
    │               └── agent-acompact-<id>.jsonl  # subagent compaction
    ├── teams/                             # team configuration
    │   └── <teamName>/
    │       └── config.json                # TeamConfig
    └── plans/                             # plan mode artifacts
        └── <slug>.md                      # plan files

Path encoding: Claude Code replaces `/` and `.` in workspace paths with
`-` to form directory names. The mapping is ambiguous; cass resolves it
by probing the filesystem (see claudecode.go:decodePath).

---

## 2. Node Types

### V1: Session

A Claude Code conversation. The fundamental unit of work.

    ID:         UUID (e.g. "9cfc19e1-5ac2-4e0e-bd19-c6e1295467d6")
    Source:     JSONL filename stem; Entry.SessionID; IndexEntry.SessionID
    Storage:    ~/.claude/projects/<encoded-path>/<sessionId>.jsonl
    Lifecycle:  created on `claude` launch; survives resume, compact, fork

A session's entry graph is a **DAG**, not a linear chain. Fork (Esc
rewind) and concurrent resume (`--resume` from multiple panes) both
create entries with the same parentUuid, producing multi-child nodes.
Orphaned branches remain in the JSONL but are unreachable from the
active leaf.

Cass indexes sessions into the `sessions` table. The cass session ID is
`SHA256(source_path)[:32]`, not the Claude session UUID.

Invariants:
- One primary JSONL file per session per project directory.
- SessionID is stable across resume, compact, and model switch.
- SessionID persists across account switches.

### V2: Entry

A single line in a JSONL session file. See `cc.Entry` in session.go.

    ID:         UUID (Entry.UUID)
    Source:     one line of a .jsonl file
    Parent:     Entry.ParentUUID (forms the DAG)

Entry types (Entry.Type):

    "user"              user or tool_result message
    "assistant"         model response (may contain tool_use blocks)
    "system"            system entries (subtype: "compact_boundary", etc.)
    "progress"          real-time status updates (hook, agent_progress, mcp_progress)
    "queue-operation"   subagent lifecycle events (enqueue, popAll)
    "file-history-snapshot"  file state before each turn

Less-common fields preserved by `cc.Entry` for callers that need raw
request, todo, and entry-linkage details:

    thinkingMetadata    extended thinking metadata
    todos               task list state (usually [])
    requestId           API request ID for assistant entries
    sourceToolAssistantUUID  back-reference from tool_result to tool_use

Attributes (not edges):
- `isSidechain: true` marks subagent entries. Never set by fork/rewind.
- `isMeta: true` marks system-injected entries (skill expansions, caveats).
- `permissionMode: "plan"` marks entries during plan mode.

### V3: SubagentSession

A Task tool invocation that runs in a child process with its own JSONL.

    ID:         agentId (short hex, e.g. "a6420de"; derived from task_id)
    Source:     <sessionId>/subagents/agent-<agentId>.jsonl
    Parent:     the Session whose Task tool_use spawned it

All entries in a subagent JSONL carry:
- `sessionId` = **parent's** session UUID (not a new session)
- `agentId` = this subagent's ID
- `isSidechain = true`

Model selection is determined by `subagent_type` in the Task tool input:

    Explore         → claude-haiku-4-5-20251001
    Plan            → claude-opus-4-6
    general-purpose → claude-opus-4-6 (default)
    Bash            → claude-haiku-4-5-20251001
    (custom)        → specified by `model` parameter

Cass records the model from the child assistant entries when present.
If those entries omit it, cass falls back to the parent Task tool input
joined through the queue notification's `tool-use-id`.

Compaction variant: `agent-acompact-<id>.jsonl` supplements (does not
replace) the original subagent file. Cass records these as
`SubagentRun{IsCompaction:true}` for auditability but excludes them from
parent FTS merging and graph fan-out to avoid duplicate content.

**Indexed by cass**: entries from primary subagent JSONL files are merged
into the parent session during collection (claudecode.go). The
`subagents/` directory is walked; `agent-acompact-*` files are indexed as
compaction metadata only.

### V4: APIRequest

A single HTTP request/response to the Anthropic Messages API,
captured from a Proxyman HAR export.

    ID:         SHA256(request_id + timestamp)[:32]
    Source:     HAR JSON file (Proxyman per-request export)
    Dedup:      source_hash = SHA256(request_id + filename)[:32]

Stored in `api_requests` table. Provides ground truth for token usage
that JSONL cannot: accurate output tokens, hidden classifier calls,
cache creation tokens, rate-limit snapshots, context composition.

### V5: OSProcess

A running `claude` process.

    ID:         pid (OS-assigned integer)
    Source:     client_pid in HAR request headers; ps/proc at runtime
    Lifecycle:  ephemeral — valid only while process runs; PID recycling
                breaks historical lookups

### V6: It2Session

An iTerm2 terminal pane.

    ID:         iTerm2 session UUID (e.g. "B8CABA4B-83D3-4484-AB38-300BAC8D8539")
    Source:     $ITERM_SESSION_ID; `it2 session list`; [it2:send-text src=XXX]
                patterns in JSONL tool results
    Lifecycle:  created on pane open; destroyed on close; stable for pane lifetime

### V7: Account

An Anthropic billing account. Extracted from the `account_<uuid>` segment
of `metadata.user_id` in API request bodies.

    ID:         account UUID (e.g. "4f876dd9-...")
    Source:     metadata.user_id in HAR request body
    Stability:  mutable per-session — changes on /account-switch

### V8: User

The human operator. Extracted from the `user_<hash>` segment of
`metadata.user_id`.

    ID:         user hash (e.g. "6312810e...")
    Source:     metadata.user_id in HAR request body
    Stability:  immutable — stable across account switches, sessions, models

### V9: RateLimitBucket

A server-side rate-limit counter scoped to an account/org.

    ID:         (account, bucket_name) where bucket_name ∈ {5h, 7d, 7d_sonnet, 7d_opus, ...}
    Source:     anthropic-ratelimit-unified-* response headers in HAR
    Lifecycle:  server-side; observed as point-in-time utilization snapshots

Per-model sub-buckets (e.g. 7d_sonnet) are present for Sonnet and Opus
but absent for Haiku.

### V10: AgentTeam

A named group of Claude Code sessions coordinating via native team
infrastructure.

    ID:         team_name (e.g. "work-team")
    Source:     Entry.TeamName; TeamConfig at ~/.claude/teams/<name>/config.json
    Lifecycle:  created by TeamCreate tool use; persists in config file

TeamConfig provides authoritative metadata:

    leadSessionId   Claude session UUID of the lead
    leadAgentId     format: "<name>@<team>" (e.g. "team-lead@work-team")
    members[]       agentId, name, model, prompt, backendType, tmuxPaneId, cwd

### V11: ForkSession

A `--fork` of an existing session. Deep-copies the entire JSONL history
into a new file with a new sessionId.

    ID:         new session UUID (e.g. "405a0cf3-...")
    Source:     new JSONL file created by `claude --resume <id> --fork`
    Parent:     the original session (linked by shared entry UUIDs in the copy)

Fork vs resume:

    --resume    appends to original JSONL; same sessionId; shared DAG
    --fork      copies history to new JSONL; new sessionId; independent DAG

The fork's first new entry has `parentUuid` pointing to a UUID that
exists in **both** files (the copied history). This is the only
structural link back to the source.

### V12: Job

A daemon-supervised Claude Code run stored at
`~/.claude/jobs/<shortId>/`. A job wraps a single Session: `shortId` is
the first 8 chars of the session UUID. The daemon writes a snapshot of
state to `state.json` and an append-only `timeline.jsonl` of state
transitions.

    ID:         shortId (8-hex prefix of sessionId)
    Source:     ~/.claude/jobs/<shortId>/state.json
    Storage:    state.json (snapshot) + timeline.jsonl (transitions)
    Lifecycle:  created when daemon launches a job; survives session reuse

Key state fields:

    sessionId       authoritative FK to V1.Session
    resumeSessionId session being resumed (often == sessionId)
    intent          user-provided prompt that triggered the job
    state           lifecycle marker ("running", "done", ...)
    tempo           cadence hint ("idle", ...)
    template        backend template ("claude")
    backend         executor ("daemon")
    cliVersion      Claude Code version at job creation
    cwd, originCwd  working dir at execution / original launch
    linkScanPath    JSONL the daemon scans for inter-session links
    linkScanOffset  byte offset of last scan (resumable)
    output.result   summary string written by the daemon at completion

Empty job directories occur transiently (job created but no state
written yet) and are skipped by collectors.

### V12b: GoalStatus (attachment)

Native Claude Code goals are emitted as Entry rows of `type:"attachment"`
with `attachment.type:"goal_status"`. The first emission for a goal sets
`sentinel:true`; subsequent emissions update `met` and may include a
`reason` string explaining the verdict. Goals are keyed by `condition`
text within a session.

    Source:     Entry.Attachment when Attachment.Type == "goal_status"
    Fields:     condition (string), met (bool), reason (string), sentinel (bool)
    Collapse:   group by condition; status=completed iff any emission has met=true

Cass extracts these into V1.Session.goals_json so they share the existing
goal_count / active_goal_count / completed_goal_count surface and FTS
indexing path with goals derived from prose-style goal blocks.

### V12c: WorkflowRun

Native Claude Code workflows are launched by an assistant `tool_use` named
`Workflow`. The parent session records the launch and task ID; the durable
fan-out artifacts live under the session sidecar directory.

    Launch:     Entry.Message.Content tool_use name == "Workflow"
    Result:     parent toolUseResult.status == "async_launched"
    State:      <session-dir>/workflows/<run_id>.json
    Agents:     <session-dir>/subagents/workflows/<run_id>/agent-*.jsonl
    Journal:    <session-dir>/subagents/workflows/<run_id>/journal.jsonl

Cass treats a workflow as a session-level summary, not as ordinary chat
history. It extracts run ID, task ID, name, description, script path,
transcript dir, status, agent count, and journal event count into
`Session.Workflows`. Native checklist tools (`TaskCreate`, `TaskUpdate`,
`TaskList`, `TaskGet`, `TaskOutput`, `TaskStop`) are counted separately from
subagent `Task` spawns.

### V13: AgentDef

A user-defined agent template stored at `~/.claude/agents/<name>.json`.
Definitions under `~/.claude/agents/.disabled/` are inactive but still
indexed. Distinct from V3 SubagentSession: AgentDef is the *template*;
SubagentSession is one runtime invocation. There is no shared runtime ID,
so Cass links the two heuristically when `SubagentRun.agent_type` exactly
matches `AgentDef.name`; list and graph views surface the matched
definition metadata when present.

    ID:         name (e.g. "sitrep-agent")
    Source:     ~/.claude/agents/[.disabled/]<name>.json
    Disabled:   path contains ".disabled/" segment

Key fields:

    description       one-line summary
    triggers          {keywords[], patterns[]} that route requests
    capabilities[]    free-form descriptions of what the agent does
    tools[]           expected Tool surface (Bash, Read, Grep, ...)
    outputFormats[]   "terminal", "json", "markdown", "audio"
    workflow[]        ordered steps the agent runs
    command           shell invocation that implements the agent
    flags             map of CLI flags the command accepts

---

## 3. Edge Types

### E1: Entry —[belongs_to]→ Session

    Join:        Entry.SessionID == Session.ID
    Cardinality: N:1
    Confidence:  authoritative
    Breaks:      never in normal operation

### E2: Entry —[child_of]→ Entry

    Join:        child.ParentUUID == parent.UUID
    Cardinality: N:1 per child; 1:N per parent (DAG, not tree)
    Confidence:  authoritative
    Breaks:      compaction replaces entry content but preserves UUIDs;
                 semantics are lossy post-compaction

Fork and concurrent resume both create entries sharing the same
parentUuid. The active branch is determined by walking backward from
the latest leaf. Orphaned branches are structurally intact but
logically abandoned.

### E3: Session —[spawns_subagent]→ SubagentSession

    Join:        queue-operation entry {task_id} matches agentId in
                 subagent filename: agent-<agentId>.jsonl
    Cardinality: 1:N
    Confidence:  authoritative (filesystem structure encodes the join)
    Breaks:      if subagent JSONL is deleted

Lifecycle signals in parent JSONL:

    queue-operation {operation: "enqueue", content: {task_id: "<agentId>"}}   → start
    queue-operation {operation: "dequeue" | "remove" | "popAll"}              → queued result consumed
    progress {data: {type: "agent_progress", agentId: "<id>"}}               → mirror
    toolUseResult {totalTokens, totalToolUseCount, totalDurationMs}          → end

### E4: SubagentSession —[mirrors_to]→ Session

    Join:        subagent entry UUID appears as parent progress entry's
                 data.message.uuid
    Cardinality: N:1
    Coverage:    ~65% of subagent entries are mirrored
    Confidence:  authoritative (UUID match is exact)
    Breaks:      ~35% of subagent entries are not mirrored (tool results,
                 intermediate entries)

The mirroring is **nested**: the parent progress entry has its own
top-level UUID; the subagent UUID is embedded in `data.message.uuid`.
There is zero overlap at the top-level UUID space.

### E5: APIRequest —[belongs_to]→ Session

    Join:        extractSessionID(apiRequest.metadata.user_id) == Session.ID
    Cardinality: N:1
    Confidence:  authoritative for all call types (response, classifier,
                 subagent, compact, quota check)
    Breaks:      subagent API calls carry the **parent's** sessionId,
                 not the subagent's agentId — see E5a

The metadata.user_id field is a composite key:

    user_<user_hash>_account_<account_uuid>_session_<session_uuid>

Parse with:

    regexp: user_([0-9a-f]+)_account_([0-9a-f-]{36})_session_([0-9a-f-]{36})

All three segments are always present. The session_uuid component is
sufficient for session linkage. The account_uuid provides billing entity
linkage (see E8). The user_hash provides operator identity (see E9).

### E5a: APIRequest —[attributed_to]→ SubagentSession

    Join:        temporal cross-reference: HAR timestamp within subagent
                 lifecycle bracket [T_enqueue, T_result] + model match
    Cardinality: N:1
    Confidence:  **heuristic** — no agentId in HAR metadata
    Breaks:      concurrent subagents (overlapping time windows with
                 same model)

Disambiguation signals:

    quota_check     Haiku, max_tokens=1, msgs=1
    classifier      Haiku, msgs=1, no tools or sparse tools
    subagent_turn   Haiku, msgs=N (growing: 3,5,7,9...)
    parent_turn     Opus/Sonnet, msgs=N

### E6: APIRequest —[made_by]→ OSProcess

    Join:        apiRequest.client_pid == process.pid
    Cardinality: N:1
    Confidence:  authoritative while process lives; ephemeral after exit
    Breaks:      PID recycling

### E7: OSProcess —[runs_in]→ It2Session

    Join:        it2 session list → match job_pid; or $ITERM_SESSION_ID
                 env var at process start
    Cardinality: N:1 (typically 1:1 for Claude)
    Confidence:  ephemeral — only queryable while process is running
    Breaks:      process exit

### E8: APIRequest —[billed_to]→ Account

    Join:        account_<uuid> segment of metadata.user_id
    Cardinality: N:1 per request
    Confidence:  authoritative per-request
    Breaks:      account switches mid-session make Session→Account a
                 non-function; each request has exactly one account,
                 but a session may span multiple

### E9: APIRequest —[made_by]→ User

    Join:        user_<hash> segment of metadata.user_id
    Cardinality: N:1
    Confidence:  authoritative; immutable
    Breaks:      never observed to change

### E10: APIRequest —[counted_against]→ RateLimitBucket

    Join:        rate-limit response headers on each APIRequest
    Cardinality: N:M (each request touches 1-3 buckets: 5h, 7d,
                 optionally 7d_<model>)
    Confidence:  authoritative snapshot — exact utilization at response time
    Breaks:      cannot decompose per-request contribution to utilization

### E11: APIRequest —[paired_with]→ APIRequest (classifier → main)

    Join:        sequential ordering within same session: Haiku classifier
                 (msgs=1) immediately precedes main model call
    Cardinality: 1:1
    Confidence:  authoritative — δ consistently 0-1 seconds, 100% reliable
    Breaks:      nothing observed; pairing is invariant

Haiku classifier subtypes:

    quota_check         max_tokens=1, content="quota"
    message_classifier  msgs=1, no tools, billing header in system
    tool_classifier     msgs=1, may include full tool schema

### E12: It2Session —[communicates_with]→ It2Session

    Join:        [it2:send-text src=XXX dst=YYY] patterns in assistant
                 tool_use blocks (Bash commands in JSONL)
    Cardinality: N:M
    Confidence:  authoritative for it2-based communication
    Sub-kinds:   message (send-text, send-key) vs observation (get-screen, get-buffer)
    Breaks:      only sender's JSONL is scanned; regex-based extraction

### E13: It2Session —[hosts]→ Session (derived)

    Join:        transitive: E7⁻¹ ∘ E6 (It2→Process→Session via PID bridge),
                 or direct from iterm_session metadata in JSONL
    Cardinality: N:M under concurrent resume (multiple It2Sessions
                 hosting the same Session)
    Confidence:  derived/heuristic
    Breaks:      ephemeral PID linkage; concurrent resume multiplicity

Stored in `session_mapping` table as (iterm_session, claude_session) pairs.

### E14: Session —[member_of(role)]→ AgentTeam

    Join:        Session.TeamName == AgentTeam.Name
    Role attr:   Session.AgentName (e.g. "researcher") or "" for lead
    Cardinality: N:1 (many sessions per team; each session in ≤1 team)
    Confidence:  authoritative
    Lead rule:   Session has TeamCreate tool_use, OR (TeamName set, AgentName empty)
    Breaks:      lead detection heuristic can misfire if member lacks AgentName

### E15: Session —[team_spawns]→ Session

    Join:        lead's Task tool_use with team_name parameter
    Evidence:    session_links row: kind="team", action="team-spawn",
                 source_session=<agent-name>, target_session=<agent-name>
    Cardinality: 1:N (one lead spawns many members)
    Confidence:  authoritative for native teams; heuristic for ccspawn
    Breaks:      session_links stores **agent names**, not session UUIDs;
                 resolution requires: find session WHERE team_name=T AND
                 agent_name=N. TeamConfig provides authoritative mapping.

### E16: Session —[team_messages]→ Session

    Join:        SendMessage/AgentMessage tool_use with recipient field
    Bidirectional: sender JSONL has tool_use; receiver JSONL has
                   <teammate-message teammate_id="sender">
    Cardinality: N:M
    Confidence:  authoritative (both sides recorded)
    Breaks:      agent-name namespace; parallel E12 (it2 IPC) invisible

### E17: Session —[forked_from]→ Session

    Join:        fork's first new entry has parentUuid pointing to a UUID
                 that exists in both the fork's copied history and the
                 original JSONL
    Cardinality: N:1 (many forks of one original)
    Confidence:  authoritative
    Breaks:      only discoverable by UUID overlap analysis across files

### E18: Job —[wraps]→ Session

    Join:        Job.sessionId == Session.ID
    Cardinality: N:1 (a session may be wrapped by multiple jobs over time,
                 e.g. successive --resume runs under the daemon)
    Confidence:  authoritative
    Breaks:      sessionId missing from state.json (early-life or corrupted
                 job directories); session not yet indexed by cass

### E19: Job —[resumes]→ Session

    Join:        Job.resumeSessionId == Session.ID
    Cardinality: N:1
    Confidence:  authoritative
    Breaks:      degenerate when resumeSessionId == sessionId (the common
                 case for fresh jobs); only meaningful when the values differ

### E20: SubagentSession —[instance_of]→ AgentDef (heuristic)

    Join:        SubagentSession.AgentType == AgentDef.Name (lossy)
    Cardinality: N:1
    Confidence:  heuristic
    Breaks:      agent types referenced by sessions but not present on disk
                 (deleted, renamed, or shipped with the CLI rather than the
                 user's ~/.claude/agents); types resolved via team config
                 instead of standalone agent definitions

---

## 4. Adjacency Matrix

Rows are source node types, columns are target node types.
Entry format: edge label (confidence).

```
              Session  Entry  Subagent  APIReq  OSProc  It2Sess  Account  User  RLBucket  Team  ForkSess
Session         E15,16  E1⁻¹   E3        E5⁻¹    —      E13⁻¹    —        —     —         E14   E17⁻¹
Entry           E1      E2     —         —       —      —        —        —     —         —     —
SubagentSess    E4      —      —         E5a⁻¹   —      —        —        —     —         —     —
APIRequest      E5      —      E5a       E11     E6     —        E8       E9    E10       —     —
OSProcess       —       —      —         E6⁻¹    —      E7       —        —     —         —     —
It2Session      E13     —      —         —       E7⁻¹   E12      —        —     —         —     —
Account         —       —      —         E8⁻¹    —      —        —        —     —         —     —
User            —       —      —         E9⁻¹    —      —        —        —     —         —     —
RLBucket        —       —      —         E10⁻¹   —      —        —        —     —         —     —
AgentTeam       E14⁻¹   —      —         —       —      —        —        —     —         —     —
ForkSession     E17     —      —         —       —      —        —        —     —         —     —
```

---

## 5. Edge Confidence Summary

| Edge | Relation                    | Confidence    | Key Risk                                    |
|------|-----------------------------|---------------|---------------------------------------------|
| E1   | Entry → Session             | authoritative | —                                           |
| E2   | Entry → Entry (parent DAG)  | authoritative | semantic loss after compaction               |
| E3   | Session → SubagentSession   | authoritative | subagent file deletion                      |
| E4   | SubagentSession → Session   | authoritative | 65% coverage; 35% entries not mirrored      |
| E5   | APIRequest → Session        | authoritative | subagent calls misattributed to parent       |
| E5a  | APIRequest → SubagentSess   | heuristic     | no agentId in HAR; temporal cross-ref only  |
| E6   | APIRequest → OSProcess      | ephemeral     | PID recycling after process exit            |
| E7   | OSProcess → It2Session      | ephemeral     | process exit                                |
| E8   | APIRequest → Account        | authoritative | account switch mid-session (non-function)   |
| E9   | APIRequest → User           | authoritative | —                                           |
| E10  | APIRequest → RateLimitBkt   | authoritative | snapshot only; no per-request decomposition |
| E11  | Classifier → Main pairing   | authoritative | —                                           |
| E12  | It2Session → It2Session     | authoritative | only sender's JSONL; regex-based            |
| E13  | It2Session → Session        | derived       | ephemeral PID bridge; concurrent resume     |
| E14  | Session → AgentTeam         | authoritative | lead heuristic misfire (missing agentName)  |
| E15  | Session → Session (spawn)   | authoritative | agent-name join, not session UUID           |
| E16  | Session → Session (message) | authoritative | agent-name namespace                        |
| E17  | Session → Session (fork)    | authoritative | UUID overlap analysis required              |

---

## 6. Communication Layer Comparison

Two independent inter-session communication mechanisms exist. They use
different identity namespaces and can operate in parallel on the same
session pair.

| Property          | E12 (it2 IPC)                     | E15/E16 (native teams)             |
|-------------------|-----------------------------------|-------------------------------------|
| Identity space    | iTerm2 session UUIDs              | agent names within team             |
| Transport         | `it2 session send-text` (shell)   | SendMessage/AgentMessage (tool_use) |
| Detection         | regex on Bash tool_use commands   | dedicated tool names                |
| Bidirectional     | sender only (receiver sees text)  | both sides (tool_use + XML tag)     |
| Cross-team        | yes                               | no (within-team only)               |
| Requires iTerm2   | yes                               | no                                  |

---

## 7. metadata.user_id Structure

The `metadata.user_id` field in API request bodies is a composite key
encoding three identities:

    Format:  user_<user_hash>_account_<account_uuid>_session_<session_uuid>
    Example: user_6312810e..._account_4f876dd9-..._session_81b3d3ea-...

| Component      | Node Type | Stability                           |
|----------------|-----------|-------------------------------------|
| user_<hash>    | V8: User  | immutable across everything         |
| account_<uuid> | V7: Acct  | mutable mid-session (/account-switch)|
| session_<uuid> | V1: Sess  | immutable for session lifetime       |

Subagent API calls carry the **parent's** metadata.user_id. There is
no agentId in the HAR data.

---

## 8. Haiku Classifier Taxonomy

Every main model call (Opus, Sonnet, or Haiku response) is preceded by
one or more Haiku classifier calls. Three distinct types:

| Type               | max_tokens | msgs | tools | Purpose                    |
|--------------------|------------|------|-------|----------------------------|
| quota_check        | 1          | 1    | none  | verify remaining quota     |
| message_classifier | 32000      | 1    | none  | classify user message      |
| tool_classifier    | 32000      | 1    | 0-69  | determine tool permissions |

Classifier-to-main pairing: sequential within session, δ = 0-1 second.
100% reliable for single-writer sessions.

---

## 9. Token Accounting Gaps

| Source                | Parent Input | Parent Output | Subagent Tokens | Classifier Tokens |
|-----------------------|-------------|---------------|-----------------|-------------------|
| JSONL (parent)        | accurate    | always 1 ¹    | aggregate only ²| invisible         |
| JSONL (subagent)      | —           | —             | accurate / 1 ¹  | invisible         |
| HAR                   | accurate    | accurate      | misattributed ³ | accurate          |
| cass index (current)  | from JSONL  | broken ¹      | merged ⁴        | zero              |

¹ JSONL stores the streaming-start snapshot (output_tokens=1), not the
  final count from SSE message_delta.

² Parent's Task tool_result has aggregate: totalTokens, totalToolUseCount,
  totalDurationMs.

³ Subagent HAR calls carry parent's session UUID. Attribution requires
  temporal cross-referencing with JSONL queue-operation timestamps.

⁴ Subagent JSONL entries are merged into the parent session record.
  Tokens are attributed to the parent session (correct for billing).
  Per-subagent breakdown requires E5a cross-referencing with HAR.

---

## 10. SQLite Schema (current)

Tables in the cass store (store.go):

    sessions            main session data + stats + FTS5 content source
    session_fts         FTS5 virtual table (title, content, agent)
    session_links       inter-session communication (it2 + team links)
    session_mapping     iTerm2 ↔ Claude session pairs
    team_configs        agent team definitions from ~/.claude/teams/
    api_requests        HAR-derived API request/response data
                        (includes user_hash, account_uuid, org_id from v3+)
    rate_limit_snapshots point-in-time rate-limit utilization
    metadata            key-value store (last_indexed_at, etc.)

See store.go:migrate() for full DDL.

---

## 11. Known Data Gaps (by severity)

### Critical

**Subagent HAR misattribution**: subagent API calls carry parent's
session UUID. No agentId in HAR. Cannot distinguish subagent vs parent
requests without temporal cross-referencing. Fix requires product
change (add agentId to API metadata).

### High

**Three-way Haiku ambiguity in HAR**: classifier, subagent, and parent
Haiku calls share the same metadata. Distinguishing requires message
count heuristic (classifier: msgs=1; subagent: msgs=N growing; parent:
msgs=N with different model). Breaks under concurrent subagents.

**Output token undercounting**: JSONL stores output_tokens=1 always
(streaming-start snapshot). Only HAR has accurate output tokens.
Historical sessions without HAR capture have permanently broken output
token accounting.

### Medium

**Team config prompt privacy**: ~/.claude/teams/ member models, prompts,
tmux pane IDs, cwd, and subscriptions are indexed into `team_members`
for queryability. UI surfaces should still avoid showing full prompts by
default because they may be long or sensitive.

**Team member mapping is heuristic outside team configs**: native team
members have authoritative roster records, but ad hoc subagent
definition links depend on exact `agent_type` / agent definition names.

### Low

**Ephemeral PID linkage**: It2Session→Session mapping (E13) depends on
PIDs that become invalid after process exit.

---

## 12. Experiments Completed

| #  | Question                           | Finding                                                 |
|----|------------------------------------|---------------------------------------------------------|
| 1  | Fork/rewind topology               | DAG branches via parentUuid; isSidechain=false; no new file |
| 2  | Concurrent resume                  | Safe interleaving; DAG branches; same JSONL file        |
| 2b | --fork vs --resume                 | Fork: deep copy + new sessionId. Resume: shared append. |
| 3  | HAR classifier/account analysis    | 3 classifier types; metadata.user_id composite; account mutable |
| 5  | Subagent/isSidechain               | isSidechain=true = subagent only; separate JSONL; parent mirrors |
| 6  | Task/todo in JSONL                 | Tasks are ephemeral tool_use/result payloads, not entries |
| 7  | Plan mode in JSONL                 | permissionMode="plan"; plan file at ~/.claude/plans/<slug>.md |
| 8  | AskUserQuestion                    | Standard tool mechanics; timestamp gap captures think time |
| 9  | Skill use / isMeta                 | isMeta=true marks system-injected entries                |
| 10 | MCP tool use                       | Standard tool mechanics; mcp_progress entries for timing |
| 11 | Workflow                           | parent launch + sidecar state/journal/agent JSONLs       |

---

## 13. Remaining Experiments

Ranked by value/cost ratio:

1. **Progress mirroring coverage** — quantify how often
   `agent_progress` messages can be linked back to subagent entries.
   Low priority unless UI needs entry-level fan-out replay.

2. **PID→It2 mapping durability** — test E7 persistence across
   subprocess exit, compaction, resume. Low priority given team infra.
