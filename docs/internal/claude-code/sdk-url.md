# Claude Code `--sdk-url` Flag

Hidden/undocumented flag that turns the Claude Code CLI into a WebSocket
client for remote programmatic control.

## Usage

```bash
claude \
  --sdk-url ws://localhost:3456/ws/cli/<session-id> \
  --print \
  --output-format stream-json \
  --input-format stream-json \
  -p "placeholder"
```

All four flags are required. The `-p` prompt argument is syntactically
required but **ignored** — the actual first prompt comes from the server
via the WebSocket.

Optional flags: `--verbose` (streaming tokens), `--model`, `--resume`,
`--continue`, `--permission-mode`, `--allowedTools`.

## What It Does

When `--sdk-url` is provided, the interactive TUI disappears and all
communication flows over NDJSON messages on a WebSocket connection. This
enables headless, remote control of Claude Code sessions from web UIs,
servers, or other tooling.

## Authentication

Sent during WebSocket upgrade via HTTP headers:

```
Authorization: Bearer <token>
X-Last-Request-Id: <uuid>       # on reconnect, for message replay
```

Token sources (priority order):
1. `CLAUDE_CODE_SESSION_ACCESS_TOKEN` env var
2. Internal session ingress token
3. File descriptor from `CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR` env var

## Connection Flow

1. CLI connects to WebSocket URL as client
2. Sends `system/init` message (capabilities, tools, models, MCP servers, cwd, version)
3. Server sends first `user` message with the actual prompt
4. Conversation loop begins

## Message Types

### Server → CLI

| Type | Purpose |
|------|---------|
| `user` | Prompts and follow-up messages |
| `control_response` | Permission approvals/denials for tool use |
| `keep_alive` | Heartbeat |
| `update_environment_variables` | Dynamic env var updates |

### CLI → Server

| Type | Purpose |
|------|---------|
| `system/init` | Initialization with capabilities |
| `assistant` | Full LLM responses |
| `stream_event` | Token-by-token chunks (if `--verbose`) |
| `result` | Query completion |
| `control_request` | Permission requests (e.g. `can_use_tool`) |
| `tool_progress` | Execution heartbeats |
| `tool_use_summary` | Tool execution details |
| `auth_status` | Authentication flow updates |

## Message Schemas

### `user` (Server → CLI)

```json
{
  "type": "user",
  "message": { "role": "user", "content": "string or ContentBlock[]" },
  "parent_tool_use_id": null,
  "session_id": ""
}
```

`session_id` is `""` for the first message. After `system/init`, the
session ID is established for subsequent messages.

### `control_request` — tool approval (CLI → Server)

```json
{
  "type": "control_request",
  "request": {
    "id": "uuid",
    "subtype": "can_use_tool",
    "tool_name": "Bash",
    "input": { }
  }
}
```

### `control_response` — tool approval (Server → CLI)

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "uuid",
    "response": {
      "behavior": "allow",
      "updatedInput": null,
      "updatedPermissions": null
    }
  }
}
```

Permission timeout: 5 minutes (auto-deny on expiry).

## Control Protocol Subtypes

1. `can_use_tool` — request tool execution approval
2. `interrupt` — server halts execution
3. `set_permission_mode` — change permission level mid-session
4. `set_model` — override model mid-session
5. `set_max_thinking_tokens` — configure extended thinking
6. `mcp_status` — MCP server status updates
7. `mcp_message` — bidirectional MCP communication
8. `hook_callback` — custom integration responses

## Internal Transport Classes

The binary contains five transport implementations:

| Class | Name | Description |
|-------|------|-------------|
| ad1 | ProcessInputTransport | Base NDJSON parser |
| LQA | SdkUrlTransport | The `--sdk-url` wrapper |
| sd1 | WebSocketTransport | Pure WebSocket |
| kQA | HybridTransport | WebSocket recv + HTTP POST send |
| fFA | DirectConnect | Browser client variant |

HybridTransport is enabled by `CLAUDE_CODE_POST_FOR_SESSION_INGRESS_V2`
env var for resilience in unstable network environments.

## Session Management

- Session ID returned in `system/init` response
- First `user` message uses `""` for `session_id`
- `--resume <id>` or `--continue` to resume conversations
- Long contexts trigger `system/compact_boundary` messages
- Query completes when `stop_reason` is `"end_turn"` with no pending tools

## Reconnection

- Automatic WebSocket reconnection with exponential backoff
- `X-Last-Request-Id` header on reconnect for message replay
- 10-second keepalive ping/pong cycle

## Relationship to Official SDK

The official Claude Agent SDK (`@anthropic-ai/claude-agent-sdk` on npm,
`claude-code-sdk` on PyPI) uses **subprocess spawning with stdio streams**,
not WebSocket. `--sdk-url` provides an alternative network transport for
remote control. The official SDK does not use or document this flag.

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `CLAUDE_CODE_SESSION_ACCESS_TOKEN` | Bearer token for WebSocket auth |
| `CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR` | File descriptor for token source |
| `CLAUDE_CODE_POST_FOR_SESSION_INGRESS_V2` | Enable hybrid transport mode |

## Community Projects

- [The Vibe Company Companion](https://github.com/The-Vibe-Company/companion) — web/mobile UI, includes full protocol docs
- [claude-code-web](https://github.com/vultuk/claude-code-web) — web interface via node-pty
- [claude-relay](https://github.com/chadbyte/claude-relay) — bridges CLI to web UI
- [claude-agent-server](https://github.com/dzhng/claude-agent-server) — sandbox-based server
- [claudecodeui](https://github.com/siteboon/claudecodeui) — remote session management UI

## Sources

- [WEBSOCKET_PROTOCOL_REVERSED.md](https://github.com/The-Vibe-Company/companion/blob/main/WEBSOCKET_PROTOCOL_REVERSED.md) — primary protocol reference
- [Reverse Engineering Claude Code](https://news.ycombinator.com/item?id=44214926) — HN discussion
