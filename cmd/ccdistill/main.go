package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccdistill"
	"github.com/tmc/cc/ccpaths"
)

// usageError is returned for flag and argument problems; main maps it to
// exit code 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

var (
	formatFlag       = flag.String("format", "anthropic", "output encoding: anthropic, openai, sharegpt")
	sinceFlag        = flag.String("since", "30d", "discovery window for session lookup")
	projectFlag      = flag.String("project", "", "substring filter on the session path")
	outFileFlag      = flag.String("o", "", "output file (NDJSON); default stdout")
	splitFlag        = flag.Float64("split", 0, "train/valid ratio in (0,1): fraction to train; 0 disables")
	outDirFlag       = flag.String("out", ".", "directory for -split output")
	minToolUsesFlag  = flag.Int("min-tool-uses", 1, "minimum tool calls to keep a sample")
	noRedactFlag     = flag.Bool("no-redact", false, "disable PII/secret redaction")
	subagentsFlag    = flag.Bool("subagents", false, "interleave subagent (sidechain) entries")
	windowFlag       = flag.Int("window", 0, "max turns per sample; longer threads are windowed (0 = off)")
	dedupFlag        = flag.Bool("dedup", false, "drop samples whose first human turn duplicates an earlier one")
	systemFlag       = flag.Bool("system", false, "prepend a synthetic system turn from session metadata")
	systemTextFlag   = flag.String("system-text", "", "override the synthetic system text (implies -system)")
	maxToolBytesFlag = flag.Int("max-tool-bytes", 0, "truncate any single tool payload to N bytes (0 = off)")
	thinkingFlag     = flag.Bool("thinking", false, "keep assistant thinking blocks as text")
	noiseFlag        = flag.Bool("drop-noise", true, "drop harness noise turns (slash-command expansions, instruction/context preambles)")
	harnessFlag      = flag.String("harness", "", "comma-separated harness allowlist for discovery: claude,codex,gemini,opencode,pi,antigravity (empty = all)")
	verboseFlag      = flag.Bool("v", false, "verbose progress and a final summary on stderr")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccdistill: %v\n", err)
		if _, ok := err.(usageError); ok {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	format, err := parseFormat(*formatFlag)
	if err != nil {
		return err
	}
	hset, err := parseHarnessFilter(*harnessFlag)
	if err != nil {
		return err
	}
	if *splitFlag != 0 && (*splitFlag <= 0 || *splitFlag >= 1) {
		return usagef("split must be in (0,1), got %v", *splitFlag)
	}

	opts := ccdistill.Options{
		System:             *systemFlag || *systemTextFlag != "",
		SystemText:         *systemTextFlag,
		DropCommands:       *noiseFlag,
		MaxToolResultBytes: *maxToolBytesFlag,
		IncludeThinking:    *thinkingFlag,
	}
	filter := ccdistill.SignalFilter(*minToolUsesFlag)

	enc, err := ccdistill.NewEncoder(format)
	if err != nil {
		return err
	}

	files, stdin, err := selectInputs(ctx, flag.Args(), hset)
	if err != nil {
		return err
	}

	out, err := newOutput(*splitFlag, *outFileFlag, *outDirFlag)
	if err != nil {
		return err
	}
	defer out.Close()

	st := stats{}
	seen := make(map[string]bool)

	process := func(file string, entries []cc.Entry) error {
		st.files++
		s, err := ccdistill.Transform(entries, opts)
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if s == nil {
			return nil
		}
		s.Meta.Source = file
		// Harness identity is derived from the on-disk location, so it is
		// unavailable for stdin (the synthetic "-" source). Leaving it empty
		// emits no harness key (omitempty) rather than the noise "unknown".
		if file != "-" {
			s.Meta.Harness = cc.Harness(file)
		}
		if !filter(s) {
			st.dropped++
			return nil
		}
		if !*noRedactFlag {
			st.redactions += ccdistill.Redact(s)
		}
		if *windowFlag > 0 {
			ccdistill.Window(s, *windowFlag)
		}
		fp := ccdistill.FingerprintFirstUser(s)
		if *dedupFlag && fp != "" {
			if seen[fp] {
				st.dropped++
				return nil
			}
			seen[fp] = true
		}
		if err := out.Write(enc, s, fp, &st); err != nil {
			return err
		}
		st.kept++
		if *verboseFlag {
			fmt.Fprintf(os.Stderr, "ccdistill: %s -> sample (%d turns, %d tool uses)\n",
				file, s.Meta.NumTurns, s.Meta.NumToolUses)
		}
		return nil
	}

	if stdin {
		entries, err := readStdin(ctx)
		if err != nil {
			return err
		}
		if err := process("-", entries); err != nil {
			return err
		}
	} else {
		for _, file := range files {
			entries, err := readSession(ctx, file, *subagentsFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ccdistill: %s: %v\n", file, err)
				continue
			}
			if err := process(file, entries); err != nil {
				return err
			}
		}
	}

	fmt.Fprintf(os.Stderr, "ccdistill: %d files, %d samples (%d train / %d valid), %d redactions\n",
		st.files, st.kept, st.train, st.valid, st.redactions)
	return nil
}

// stats accumulates run counters for the final summary.
type stats struct {
	files      int
	kept       int
	dropped    int
	train      int
	valid      int
	redactions int
}

// parseFormat validates and converts the -format flag.
func parseFormat(s string) (ccdistill.Format, error) {
	switch ccdistill.Format(s) {
	case ccdistill.FormatAnthropic, ccdistill.FormatOpenAI, ccdistill.FormatShareGPT:
		return ccdistill.Format(s), nil
	default:
		return "", usagef("unknown format %q (want anthropic, openai, or sharegpt)", s)
	}
}

// parseHarnessFilter parses the -harness allowlist. An empty string returns
// (nil, nil), meaning no filtering. Unknown names are a usage error. The
// sentinel "unknown" is deliberately not accepted: discovery cannot be asked
// for unclassifiable files.
func parseHarnessFilter(s string) (map[string]bool, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	valid := map[string]bool{
		"claude": true, "codex": true, "gemini": true,
		"opencode": true, "pi": true, "antigravity": true,
	}
	set := make(map[string]bool)
	for name := range strings.SplitSeq(s, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !valid[name] {
			return nil, usagef("unknown harness %q (want claude, codex, gemini, opencode, pi, or antigravity)", name)
		}
		set[name] = true
	}
	if len(set) == 0 {
		return nil, nil
	}
	return set, nil
}

// filterByHarness keeps only the discovered files whose harness, as classified
// by cc.Harness, is in set.
func filterByHarness(files []string, set map[string]bool) []string {
	kept := files[:0]
	for _, f := range files {
		if set[cc.Harness(f)] {
			kept = append(kept, f)
		}
	}
	return kept
}

// selectInputs resolves the positional arguments into a list of session files
// or a single stdin flag. With no arguments it discovers sessions, post-filtered
// by hset when non-nil. The harness filter applies only to discovery: explicit
// file arguments and stdin are passed through untouched.
func selectInputs(ctx context.Context, args []string, hset map[string]bool) (files []string, stdin bool, err error) {
	for _, a := range args {
		if a == "-" {
			if len(args) != 1 {
				return nil, false, usagef(`"-" cannot be combined with other file arguments`)
			}
			return nil, true, nil
		}
	}
	if len(args) == 0 {
		since, err := ccpaths.ParseDuration(*sinceFlag)
		if err != nil {
			return nil, false, usagef("invalid -since %q: %v", *sinceFlag, err)
		}
		files, err = cc.FindSessionFiles(ctx, since, *projectFlag)
		if err != nil {
			return nil, false, fmt.Errorf("discover sessions: %w", err)
		}
		if hset != nil {
			files = filterByHarness(files, hset)
		}
		return files, false, nil
	}
	for _, a := range args {
		fi, statErr := os.Stat(a)
		if statErr == nil && fi.IsDir() {
			walked, walkErr := walkSessions(a)
			if walkErr != nil {
				return nil, false, walkErr
			}
			files = append(files, walked...)
			continue
		}
		files = append(files, a)
	}
	return files, false, nil
}

// walkSessions collects *.jsonl files under dir.
func walkSessions(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return out, nil
}

// readSession reads one session file, optionally interleaving subagent entries.
func readSession(ctx context.Context, file string, subagents bool) ([]cc.Entry, error) {
	if subagents {
		return cc.ReadFileWithSubagents(ctx, file)
	}
	return cc.ReadFile(ctx, file)
}

// readStdin copies stdin to a temporary file and reads it as one session, so
// the cc reader's multi-format detection applies uniformly.
func readStdin(ctx context.Context) ([]cc.Entry, error) {
	tmp, err := os.CreateTemp("", "ccdistill-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("buffer stdin: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, os.Stdin); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("buffer stdin: %w", err)
	}
	return cc.ReadFile(ctx, tmp.Name())
}

// output writes encoded records, either to a single stream or to split files.
type output interface {
	Write(enc ccdistill.Encoder, s *ccdistill.Sample, fingerprint string, st *stats) error
	Close() error
}

// newOutput builds the output sink: a deterministic train/valid splitter when
// split is set, otherwise a single NDJSON stream.
func newOutput(split float64, outFile, outDir string) (output, error) {
	if split != 0 {
		return newSplitOutput(split, outDir)
	}
	if outFile == "" {
		return &streamOutput{w: os.Stdout}, nil
	}
	f, err := os.Create(outFile)
	if err != nil {
		return nil, fmt.Errorf("create output: %w", err)
	}
	return &streamOutput{w: f, c: f}, nil
}

// streamOutput writes every record to one writer.
type streamOutput struct {
	w io.Writer
	c io.Closer
}

func (o *streamOutput) Write(enc ccdistill.Encoder, s *ccdistill.Sample, _ string, st *stats) error {
	if err := writeRecord(o.w, enc, s); err != nil {
		return err
	}
	st.train++ // a non-split run counts every record under "train" for the summary.
	return nil
}

func (o *streamOutput) Close() error {
	if o.c != nil {
		return o.c.Close()
	}
	return nil
}

// splitOutput routes each record to train.jsonl or valid.jsonl by a stable hash
// of its fingerprint, so the assignment is reproducible across runs.
type splitOutput struct {
	ratio float64
	train *os.File
	valid *os.File
}

func newSplitOutput(ratio float64, dir string) (*splitOutput, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create out dir: %w", err)
	}
	train, err := os.Create(filepath.Join(dir, "train.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("create train.jsonl: %w", err)
	}
	valid, err := os.Create(filepath.Join(dir, "valid.jsonl"))
	if err != nil {
		train.Close()
		return nil, fmt.Errorf("create valid.jsonl: %w", err)
	}
	return &splitOutput{ratio: ratio, train: train, valid: valid}, nil
}

func (o *splitOutput) Write(enc ccdistill.Encoder, s *ccdistill.Sample, fingerprint string, st *stats) error {
	if assignTrain(fingerprint, o.ratio) {
		st.train++
		return writeRecord(o.train, enc, s)
	}
	st.valid++
	return writeRecord(o.valid, enc, s)
}

func (o *splitOutput) Close() error {
	err := o.train.Close()
	if verr := o.valid.Close(); verr != nil && err == nil {
		err = verr
	}
	return err
}

// assignTrain reports whether a record with the given fingerprint belongs to
// the train split, using a stable hash so the split is reproducible. A record
// with no fingerprint falls back to the encoded bytes via an empty string,
// which still hashes deterministically.
func assignTrain(fingerprint string, ratio float64) bool {
	sum := sha256.Sum256([]byte(fingerprint))
	v := binary.BigEndian.Uint64(sum[:8])
	frac := float64(v) / float64(^uint64(0))
	return frac < ratio
}

// writeRecord encodes s and appends a newline, producing one NDJSON line.
func writeRecord(w io.Writer, enc ccdistill.Encoder, s *ccdistill.Sample) error {
	if err := enc.Encode(w, s); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
