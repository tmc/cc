// Command ccdistill turns local agent session histories into SFT records.
//
// ccdistill is a thin wrapper over the github.com/tmc/cc reader and the
// github.com/tmc/cc/ccdistill library. It discovers session files (or reads
// the ones named on the command line), folds each session into a normalized
// trajectory, runs a small data-prep pipeline (filter, redact, window,
// dedup), and writes one JSON record per session as NDJSON.
//
// # Usage
//
//	ccdistill [flags] [file ...]
//
// With no file arguments, ccdistill discovers recent sessions across all
// agent homes via cc.FindSessionFiles, honoring -since and -project. File
// arguments transform exactly those session files; a directory argument is
// walked for *.jsonl. A single "-" reads one session's JSONL from stdin.
//
// # Flags
//
//	-format FMT       Output encoding: anthropic (default), openai, sharegpt.
//	-since DUR        Discovery window (default 30d); supports d, w, h.
//	-project SUB      Substring filter on the session path.
//	-o FILE           Write NDJSON to FILE (default stdout). Ignored with -split.
//	-split R          Train/valid ratio in (0,1): fraction to TRAIN. 0 disables.
//	-out DIR          Directory for -split output (default ".").
//	-min-tool-uses N  Minimum tool calls to keep a sample (default 1).
//	-no-redact        Disable PII/secret redaction (on by default).
//	-subagents        Interleave subagent (IsSidechain) entries.
//	-window N         Head/tail-window samples longer than N turns (0 = off).
//	-dedup            Drop samples whose first human turn duplicates an earlier one.
//	-system           Prepend a synthetic system turn from session metadata.
//	-system-text TEXT Override the synthetic system text (implies -system).
//	-max-tool-bytes N Truncate any single tool payload to N bytes (0 = off).
//	-thinking         Keep assistant thinking blocks as text.
//	-v                Verbose progress and a final summary on stderr.
//
// # Formats
//
//	anthropic  messages[] with tool_use and tool_result blocks kept verbatim.
//	openai     {messages:[{role,content}]}, tool calls flattened to text.
//	sharegpt   {conversations:[{from,value}]}.
//
// # Output
//
// Records are NDJSON: one encoded record plus a newline per surviving sample,
// streamed to -o or stdout. With -split R, each record is assigned
// deterministically (by a hash of the first human turn) to train.jsonl with
// probability R, else valid.jsonl, both written under -out.
//
// # Examples
//
// Distill the last 30 days of sessions to stdout as Anthropic records:
//
//	ccdistill > sft.jsonl
//
// Build an OpenAI-format 90/10 train/valid split for one project:
//
//	ccdistill -format openai -project myapp -split 0.9 -out data
//
// Transform a single session read from stdin:
//
//	cat session.jsonl | ccdistill -
//
// # Exit Codes
//
//	0   Success (including zero samples produced).
//	1   I/O or discovery failure.
//	2   Usage error (unknown -format, bad -since, -split out of range,
//	    or "-" combined with other file arguments).
package main
