# Claude waiting hook

`ccsessions -waiting` reads JSONL state from `~/.cc/waiting.jsonl`. Install
`scripts/claude-waiting-hook.sh` somewhere stable, then add this entry to the
existing `Notification` hook list in `~/.claude/settings.json`:

```json
{
  "type": "command",
  "command": "/absolute/path/to/cc/scripts/claude-waiting-hook.sh"
}
```

The hook records notifications whose text contains the Claude question-dialog
footer. It writes a clearing record for other notifications. This is an
optional local integration; `cc` does not modify Claude settings.
