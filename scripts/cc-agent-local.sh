#!/usr/bin/env bash
# cc-agent-local.sh — Run Claude Code against a local Ollama instance.
#
# Usage:
#   cc-agent-local.sh [claude-args...]
#
# Environment:
#   OLLAMA_HOST   Ollama base URL (default: http://localhost:11434)
#   OLLAMA_MODEL  Model to use (default: gpt-oss)
#
# Prerequisites:
#   - ollama must be installed and running
#   - A model with large context (>=64k) is recommended
#
# Recommended models: qwen3-coder, glm-4.7, gpt-oss:20b, gpt-oss:120b
#
# See: https://docs.ollama.com/integrations/claude-code

set -euo pipefail

OLLAMA_HOST="${OLLAMA_HOST:-http://localhost:11434}"
OLLAMA_MODEL="${OLLAMA_MODEL:-gpt-oss}"

# Verify ollama is reachable.
if ! curl -sf "${OLLAMA_HOST}/api/version" >/dev/null 2>&1; then
	echo "error: ollama not reachable at ${OLLAMA_HOST}" >&2
	echo "Start it with: ollama serve" >&2
	exit 1
fi

# Ensure the model is available locally; pull if missing.
if ! ollama list 2>/dev/null | grep -q "^${OLLAMA_MODEL}"; then
	echo "Pulling model ${OLLAMA_MODEL}..." >&2
	ollama pull "${OLLAMA_MODEL}"
fi

exec env \
	ANTHROPIC_AUTH_TOKEN=ollama \
	ANTHROPIC_API_KEY="" \
	ANTHROPIC_BASE_URL="${OLLAMA_HOST}" \
	DISABLE_TELEMETRY=1 \
	claude --model "${OLLAMA_MODEL}" "$@"
