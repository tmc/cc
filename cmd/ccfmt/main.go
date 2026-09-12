// Command ccfmt formats a coding-agent session for reading or publication.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tmc/cc/internal/ccfmt"
)

func main() {
	opts := ccfmt.Default()
	flag.StringVar(&opts.Format, "format", opts.Format, "Output format: transcript, text, markdown, json, jsonl")
	flag.IntVar(&opts.Width, "width", opts.Width, "Wrap width for -format transcript (0 uses $COLUMNS, else 100)")
	flag.StringVar(&opts.Title, "title", opts.Title, "Optional document title")
	flag.BoolVar(&opts.Meta, "meta", opts.Meta, "Include session metadata and injected preamble messages")
	flag.BoolVar(&opts.Timestamps, "timestamps", opts.Timestamps, "Include timestamps in formatted output")
	flag.IntVar(&opts.MaxBytes, "max-bytes", opts.MaxBytes, "Maximum bytes to include from a single text or tool-result block; 0 means unlimited")
	flag.StringVar(&opts.Cleanup, "cleanup", opts.Cleanup, "Cleanup preset: none, publish, digest")
	flag.BoolVar(&opts.Commentary, "commentary", opts.Commentary, "Include commentary-phase assistant messages")
	flag.StringVar(&opts.Tools, "tools", opts.Tools, "Tool call rendering: full, summary, omit")
	flag.StringVar(&opts.ToolResults, "tool-results", opts.ToolResults, "Tool result rendering: full, summary, omit")
	flag.BoolVar(&opts.InlineImages, "inline-images", opts.InlineImages, "Inline message images as data URLs in markdown when possible")
	flag.BoolVar(&opts.RedactImages, "redact-images", opts.RedactImages, "Redact sensitive text from inline images (requires an external redactor; default no-op)")
	flag.BoolVar(&opts.Redact, "redact", opts.Redact, "Redact obvious secrets such as Stripe test keys")
	flag.StringVar(&opts.Since, "since", opts.Since, "How far back to search when resolving a session ID")
	flag.Parse()

	// A cleanup preset fills in only what the command line left alone.
	opts.Explicit = make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { opts.Explicit[f.Name] = true })

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "ccfmt: %v\n", err)
		os.Exit(1)
	}
}

func run(opts ccfmt.Options) error {
	f, err := ccfmt.New(opts)
	if err != nil {
		return err
	}
	return f.Format(os.Stdout, flag.Args())
}
