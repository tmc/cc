// Command ccfmt formats coding-agent session transcripts.
//
// ccfmt reads Claude Code, Codex, opencode, or pi session files (or stdin)
// and renders them in cleaner formats for humans. The markdown format is
// intended for publishing transcripts on GitHub or GitHub Pages.
//
// # Usage
//
//	ccfmt [flags] [session.jsonl ...]
//
// With no positional arguments, ccfmt reads from stdin. Subagent
// JSONL files alongside the session are loaded automatically.
//
// # Flags
//
//	-format FMT          Output format: text (default), markdown/md, json, jsonl.
//	-title TITLE         Optional document title (markdown only).
//	-meta                Keep injected preamble and session metadata entries.
//	-timestamps          Include per-entry timestamps in the output.
//	-cleanup PRESET      Apply a preset that adjusts -tools/-tool-results/-max-bytes:
//	                     none (default), publish, digest.
//	-tools MODE          Tool-call rendering: full (default), summary, omit.
//	-tool-results MODE   Tool-result rendering: full (default), summary, omit.
//	-commentary          Include commentary-phase assistant messages (default true).
//	-max-bytes N         Truncate any text or tool-result block past N bytes
//	                     (default 4000; 0 disables truncation).
//	-inline-images       Inline message images as base64 data URLs (default true).
//	-redact              Redact obvious secrets (e.g. Stripe test keys, default true).
//	-redact-images       Pass image bytes through redactImageDataFunc (default no-op).
//
// # Examples
//
// Render a session as a publishable markdown post:
//
//	ccfmt -format markdown -cleanup publish session.jsonl > session.md
//
// Pipe filtered JSONL through another tool:
//
//	ccsessions -format jsonl | ccfmt -format jsonl
//
// Get a terminal-friendly transcript with timestamps:
//
//	ccfmt -timestamps session.jsonl
package main
