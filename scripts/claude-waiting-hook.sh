#!/bin/sh
# claude-waiting-hook records Claude notification state for ccsessions.

set -eu

state=${CC_WAITING_FILE:-"$HOME/.cc/waiting.jsonl"}
input=$(cat)
session=$(printf '%s' "$input" | jq -r '.session_id // empty')
[ -n "$session" ] || exit 0

message=$(printf '%s' "$input" | jq -r '.message // .notification // empty')
case "$message" in
*"Enter to select"* | *"Submit"*) waiting=true ;;
*) waiting=false ;;
esac

mkdir -p "$(dirname "$state")"
jq -cn --arg session "$session" --arg message "$message" --argjson waiting "$waiting" \
	'{session_id:$session, questions:([$message] | map(select(. != ""))), waiting:$waiting}' >>"$state"
