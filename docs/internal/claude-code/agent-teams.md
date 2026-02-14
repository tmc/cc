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

## Backend Types

Members can use different backends:
- `tmux` - tmux pane (default for teams)
- (future: `iterm2`, `process`)

## Stats Extraction

The `cass/collector/stats.go` detects team interactions through:

1. **Native tools**: `TeamCreate`, `SendMessage`, `AgentMessage`, `AgentTask`
2. **Bash commands**: `ccinbox`, `ccteam`, `cctl spawn`, `cctask`, `ccspawn`

Mapped to `SessionStats` fields:
- `TeamSpawns` - `TeamCreate` calls + `ccspawn` commands
- `TeamInboxSends` - `SendMessage`/`AgentMessage` calls + `ccinbox send`
- `TeamInboxReads` - `ccinbox read/list` commands
- `TeamTaskOps` - `AgentTask` calls + `cctask` commands
