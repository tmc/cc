// Command ccfmt formats coding session transcripts.
//
// It reads Claude Code and Codex session JSONL and renders them in cleaner
// formats for humans. The markdown format is intended for publishing session
// transcripts on GitHub or GitHub Pages.
//
// # Usage
//
//	ccfmt [flags] [session.jsonl]
//	ccfmt -format markdown session.jsonl > session.md
//	ccsessions -format jsonl | ccfmt
//	ccfmt -format markdown -cleanup publish session.jsonl > session.md
//
// # Formats
//
//	text
//	    Compact readable transcript for terminal output.
//
//	markdown
//	    GitHub-friendly transcript with headings and fenced code blocks.
//
//	json
//	    Filtered session entries as pretty JSON.
//
//	jsonl
//	    Filtered session entries as newline-delimited JSON.
//
// By default, ccfmt omits injected system preambles and session metadata.
// Use -meta to keep those entries in the output.
//
// # Cleanup
//
// Use -cleanup publish to generate a cleaner transcript for publication.
// That preset summarizes tool calls and tool results instead of emitting full
// fenced blocks for every command and command output.
package main
