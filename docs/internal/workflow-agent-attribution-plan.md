# Workflow-agent attribution — implementation plan

Goal: workflow agent transcripts stay indexed/searchable and reachable, but are
NEVER top-level sessions. Search-list folds them under the parent ("matched in N
agents"); parent detail renders a workflow tree with per-agent transcripts.

Verified by 3 adversarial reviewers. The keystone they all found: the fix is NOT
just the collector skip — `parentSessionPath` (sse.go) only collapses ONE level,
so the watcher never re-indexes the parent for a workflow-agent change. Must fix
both sides symmetrically (same "first subagents segment" notion).

## Layers (build green after each)

### 1. KEYSTONE — watcher parent-derivation (cass/web/sse.go)
`parentSessionPath` must collapse from the FIRST `subagents` segment:
`<...>/<uuid>/subagents/.../agent-x.jsonl` → `<...>/<uuid>.jsonl`. Handles both
flat `subagents/agent-*.jsonl` and nested `subagents/workflows/wf_*/agent-*.jsonl`.
Test: parentSessionPath of a nested workflow-agent path == parent .jsonl.

### 2. COLLECTOR skip (cass/collector/claudecode.go)
- Add `underSubagents(path)` segment-aware predicate.
- In scanDir: keep the `info.Name()=="subagents"` SkipDir prune (fast path for
  full scans) AND add `if !info.IsDir() && underSubagents(path) { return nil }`
  before parsing, so a walk rooted below subagents/ still emits nothing.

### 3. COLLECTOR fold + per-agent metadata (claudecode.go + workflows.go)
- parseSession: after the existing flat-subagent merge, glob
  `<uuid>/subagents/workflows/wf_*/agent-*.jsonl` (skip acompact), read via
  readSessionFile, append to `entries` (merged content, NOT parentEntries).
- workflows.go: replace countWorkflowAgents with readWorkflowAgents(dir) →
  []cass.WorkflowAgent (ID from agent-<id>.jsonl, Title=FirstPrompt, ToolCalls,
  Tokens, SourcePath, Status). Assign w.Agents + w.AgentCount=len(Agents).

### 4. DATA MODEL (cass/workflow.go)
- Add WorkflowAgent{ID,Title,ToolCalls,Tokens,SourcePath,Status}.
- Add WorkflowRun.Agents []WorkflowAgent.

### 5. STORE (cass/store/workflows.go + store.go)
- Migration: `ALTER TABLE workflows ADD COLUMN agents_json TEXT NOT NULL DEFAULT '[]'`.
- WorkflowRow.AgentsJSON; bind json.Marshal(wf.Agents) in BatchIndex wfStmt;
  SELECT+Scan in Workflows(), WorkflowsSince(), foldWorkflows (unmarshal→w.Agents).
  (Four SELECT/Scan sites in lockstep.)

### 6. SEARCH badge (cass/store/store.go foldWorkflows)
- When query present, match each agent (lowercased Title) → append agent.ID to
  h.MatchedWorkflowAgentIDs, set CollapsedChildren, bump WorkflowMatchCount by
  matched-agent count (fall back to run-level when only run meta matched).
- Part 1 (parent surfaces agent body text) is delivered by layer 3 folding.

### 7. API (cass/web/server.go + api.go)
- `GET /api/workflow-agent?source_path=...` → validateWorkflowAgentPath:
  filepath.Clean, require `/subagents/workflows/` segment, EvalSymlinks +
  HasPrefix(ClaudeHome()/projects), require agent-*.jsonl. Then SSE-or-JSON via
  streamSession / readSessionEntries (single file, NO subagent merge).
- Extend readSessionEntriesWithSubagents to also walk subagents/workflows/wf_*/
  so the parent's flat stream includes workflow-agent entries (badged sidechain).

### 8. UI tree (cass/web/static/index.html) — Task #44
- SessionDetail reads hit.workflows; render workflow run → agents tree; each
  agent row fetches api/workflow-agent?source_path=<a.source_path> (SSE) inline.

### 9. PURGE (cass/store/store.go init)
- After migrations: SELECT id FROM sessions WHERE source_path LIKE '%/subagents/%',
  route through DB.Delete(IDs) in batches (tx deletes subagent_runs, workflows,
  sessions; sessions_ad trigger syncs session_fts). Idempotent once layer 1 lands.
- Verify: count → 0; nested-workflow child row cascade handled.

## Risks tracked
- Incremental cache won't invalidate parent on agent-only change → layer 1 maps
  agent path to parent so the parent .jsonl is in the scan set; cache keys on the
  parent file so its own grow/skip logic applies (parent re-parse re-globs agents).
- FTS/content growth from folding agent text — accept (mirrors plain-subagent).
- Per-agent Tokens use streaming-start snapshot — approximate, document.
- /api/workflow-agent path-traversal — symlink-resolved prefix check.
