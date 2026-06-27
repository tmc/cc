// Package ccdistill turns local agent session histories into supervised
// fine-tuning (SFT) records.
//
// A session is read with the [github.com/tmc/cc] reader, which normalizes
// Claude Code, Codex, Gemini, opencode, pi, and antigravity sessions into a
// single [cc.Entry] shape. [Transform] folds one session's ordered entries
// into a [Sample]: a normalized list of [Turn]s (an optional system turn, then
// alternating user and assistant turns) with tool calls kept on assistant
// turns and tool results kept on the following user turn.
//
// The package is pure transform and encode logic; it performs no I/O. Callers
// own discovery, reading, and writing, which keeps the pipeline testable.
//
// # Pipeline
//
// A caller drives the Sample-level stages in order:
//
//   - [Transform] builds a Sample from one session's entries in file order.
//   - [Filter] (see [SignalFilter]) drops Samples without enough signal.
//   - [Redact] scrubs secrets and PII in place.
//   - [Window] head/tail-trims very long threads.
//   - [FingerprintFirstUser] enables dedup across a run.
//   - [NewEncoder] returns an [Encoder] for the chosen [Format].
//
// # Formats
//
// Three output encodings are supported:
//
//   - [FormatAnthropic]: messages[] with tool_use and tool_result blocks kept
//     verbatim.
//   - [FormatOpenAI]: {messages:[{role,content}]}, tool calls flattened to
//     text.
//   - [FormatShareGPT]: {conversations:[{from,value}]}.
//
// # Usage
//
//	entries, _ := cc.ReadFile(ctx, path)
//	s, _ := ccdistill.Transform(entries, ccdistill.Options{})
//	if s != nil && ccdistill.SignalFilter(1)(s) {
//		ccdistill.Redact(s)
//		enc, _ := ccdistill.NewEncoder(ccdistill.FormatAnthropic)
//		enc.Encode(os.Stdout, s)
//		os.Stdout.Write([]byte("\n"))
//	}
package ccdistill
