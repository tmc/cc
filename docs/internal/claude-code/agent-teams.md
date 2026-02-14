# Claude Agent Teams

Documentation based on inspection of live team session data from
`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=true` sessions.

## Overview

Agent teams allow a Claude Code session (the "team lead") to spawn and
coordinate multiple Claude agents as teammates. Each teammate runs as a
separate Claude session, connected via an inbox-based messaging system.

## Architecture

### File Locations

```
~/.claude/teams/<team-name>/
  config.json           # Team configuration and member list
  inboxes/
    <member-name>.json  # Inbox messages for each member
```

### Team Config (`config.json`)

```json
{
  "name": "work-team",
  "description": "General-purpose work team with 3 members",
  "createdAt": 1771105155195,
  "leadAgentId": "team-lead@work-team",
  "leadSessionId": "dfd9229a-...",
  "members": [
    {
      "agentId": "team-lead@work-team",
      "name": "team-lead",
      "agentType": "team-lead",
      "model": "claude-opus-4-6",
      "joinedAt": 1771105155195,
      "cwd": "/path/to/workspace",
      "subscriptions": []
    },
    {
      "agentId": "researcher@work-team",
      "name": "researcher",
      "agentType": "general-purpose",
      "model": "claude-opus-4-6",
      "prompt": "You are \"researcher\", a member of...",
      "color": "blue",
      "backendType": "tmux",
      "tmuxPaneId": "%1",
      "isActive": false,
      "subscriptions": []
    }
  ]
}
```

### Inbox Messages (`inboxes/<name>.json`)

Array of message objects:

```json
[
  {
    "from": "team-lead",
    "text": "Your initial prompt...",
    "timestamp": "2026-02-14T21:39:22.402Z",
    "read": true
  },
  {
    "from": "researcher",
    "text": "Hi, researcher ready for work.",
    "summary": "Researcher ready, no tasks yet",
    "timestamp": "2026-02-14T21:39:33.649Z",
    "color": "blue",
    "read": true
  }
]
```

## Session JSONL Entries

### Team Lead Session

The team lead uses native tool names (not Bash commands):

| Tool Name | Purpose |
|-----------|---------|
| `TeamCreate` | Create a new team |
| `SendMessage` | Send a message to a teammate |
| `AgentMessage` | Alternative message-sending tool |
| `AgentTask` | Create/manage tasks for teammates |
| `Task` | Spawn teammate sessions (one per member) |

Example entry flow:
1. User: "start a claude agent team with 3 members"
2. Assistant: uses `TeamCreate` tool
3. Assistant: uses `Task` tool x3 to spawn members
4. User: `<teammate-message>` XML arrives as member responses

### Teammate Messages

Teammate messages arrive in the team lead session as user messages
with XML-formatted content:

```xml
<teammate-message teammate_id="researcher" color="blue"
    summary="Researcher ready, no tasks yet">
Hi, I'm researcher, ready for work.
</teammate-message>
```

Idle notifications are JSON:
```xml
<teammate-message teammate_id="builder" color="green">
{"type":"idle_notification","from":"builder","timestamp":"2026-02-14T21:40:00Z"}
</teammate-message>
```

### Member Sessions

Each member session receives its initial prompt wrapped in
`<teammate-message>` XML from the team lead. Members have:

- `teamName` field on entries (e.g. "work-team")
- `agentName` field on entries (e.g. "reviewer")
- A `stop_hook_summary` system subtype at session end

### Entry Fields for Teams

```go
type Entry struct {
    // ... standard fields ...
    TeamName  string `json:"teamName,omitempty"`
    AgentName string `json:"agentName,omitempty"`
}
```

### System Subtypes

| Subtype | Context | Description |
|---------|---------|-------------|
| `stop_hook_summary` | Member sessions | Records hook execution at session stop |

The `stop_hook_summary` entry contains:
- `hookCount` - number of hooks run
- `hookInfos` - array of `{command: "function"}`
- `hookErrors` - any errors
- `preventedContinuation` - whether hooks blocked continuation

## Task Files

Team tasks are stored as shared JSON files keyed by team name:

```
~/.claude/tasks/<team-name>/
  .lock              # Coordination lock
  1.json             # Task 1
  2.json             # Task 2
  ...
```

Each task file:
```json
{
  "id": "1",
  "subject": "Add cc dependency to go.mod",
  "description": "...",
  "activeForm": "Adding cc dependency",
  "owner": "team-lead",
  "status": "completed",
  "blocks": ["2", "3"],
  "blockedBy": []
}
```

Team tasks use the team name as directory key (not a UUID), enabling
shared access across all team members. Regular (non-team) session
tasks use session UUIDs as directory keys.

Task assignments are also sent via inbox as JSON messages:
```json
{"type":"task_assignment","taskId":"1","subject":"...","assignedBy":"team-lead"}
```

## Lifecycle

Team directories (`~/.claude/teams/<name>/`) persist after member
sessions end (`isActive: false`). Cleanup occurs when the lead session
terminates gracefully. Abandoned/killed lead sessions leave the team
directory in place.

## Backend Types

Members can use different backends:
- `tmux` - tmux pane (default for teams)
- (future: `iterm2`, `process`)

## Retroactive Detection (CASS)

The `cass/collector/` package detects team activity from JSONL session
files alone (no external config/task files needed).

### Role Classification (`ClassifyTeamRole`)

| Signal | Lead | Member |
|--------|------|--------|
| `teamName` field | Present after TeamCreate | Present on all entries |
| `agentName` field | Always absent | Always present |
| `TeamCreate` tool use | Definitive lead signal | Never |
| First entry | Normal user prompt | `<teammate-message>` from team-lead |

### Stats Extraction (`ExtractStats`)

Native tool detection:

| Tool | Stat Field |
|------|-----------|
| `TeamCreate` | `TeamSpawns` |
| `SendMessage` / `AgentMessage` | `TeamInboxSends` |
| `AgentTask` | `TeamTaskOps` |
| `Task` with `team_name` input | `TeamMembersSpawned` |
| `<teammate-message>` XML in user content | `TeamMessagesRecvd` |

Bash command detection:

| Pattern | Stat Field |
|---------|-----------|
| `ccspawn`, `cctl spawn` | `TeamSpawns` |
| `ccinbox send/append` | `TeamInboxSends` |
| `ccinbox read/list`, `cctl inbox` | `TeamInboxReads` |
| `cctask`, `cctl task` | `TeamTaskOps` |

### Team Link Extraction (`ExtractTeamLinks`)

Creates `SessionLink` entries with `Kind: "team"`:

| Action | Source | Target | When |
|--------|--------|--------|------|
| `team-spawn` | lead | member name | `Task` tool with `team_name` |
| `team-message` | sender | recipient | `SendMessage`/`AgentMessage` |
| `team-message` | teammate_id | self | `<teammate-message>` XML |

Links are deduplicated per direction per pair.

### Ad-Hoc IT2 Teams

Sessions using `it2 session split` (strong signal) and `it2 session
send-text` (communication signal) are detected as ad-hoc team
participants. These produce standard it2-based `SessionLink` entries
with `Kind: "message"` / `"observation"`.

### Structural Differences: Lead vs Member

| Field | Lead Session | Member Session |
|-------|-------------|----------------|
| `teamName` | After TeamCreate only | All entries from start |
| `agentName` | Always absent | Always present |
| `IsTeamLead` | `true` | `false` |
| First entry | Normal user prompt | `<teammate-message>` from team-lead |
| Session ends with | Normal response | `stop_hook_summary` |
