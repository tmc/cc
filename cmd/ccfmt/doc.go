// Command ccfmt formats coding-agent session transcripts.
//
// ccfmt reads Claude Code, Codex, opencode, or pi session files (or stdin)
// and renders them in formats for reading or export. Transcript is the
// default reading format. Text is a raw diagnostic format; markdown is for
// publishing transcripts on GitHub or GitHub Pages.
//
// # Usage
//
//	ccfmt [flags] [session.jsonl | SESSION_ID ...]
//
// With no positional arguments, ccfmt reads from stdin. An argument that is
// not an existing file is treated as a session ID (or ID prefix) and resolved
// against the harness session stores, the same lookup ccrender uses. Subagent
// JSONL files alongside the session are loaded automatically.
//
// # Flags
//
//	-format FMT          Output format: transcript (default), text (raw), markdown/md,
//	                     json, jsonl. The transcript format reproduces the
//	                     recording harness's own TUI (glyphs, folded tool
//	                     results, color) and ignores the document flags below.
//	-width N             Wrap width for -format transcript (0 uses $COLUMNS,
//	                     else 100).
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
//	-redact-images       Pass image bytes through the image redactor (default no-op).
//	-since DUR           How far back to search when resolving a session ID
//	                     (default 30d).
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
// Get a terminal-friendly transcript with timestamps (the default format):
//
//	ccfmt -timestamps session.jsonl
//
// Reproduce the harness's own TUI rendering (what ccrender shows):
//
//	ccfmt -format transcript session.jsonl | less -R
//
// Render a session by ID instead of path:
//
//	ccfmt -format text c82a34fb
package main
