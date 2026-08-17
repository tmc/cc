package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/tmc/cc/cchooks"
)

var (
	agentFlag = flag.String("agent", "", "Only report this agent (claude-code, codex)")
	jsonFlag  = flag.Bool("json", false, "Emit JSON instead of a table")
	dirFlag   = flag.String("dir", "", "Include project settings from these directories (comma-separated; default cwd)")
	allFlag   = flag.Bool("v", false, "Include informational diagnostics")
	rootFlag  = flag.String("store", "", "Event store root to inspect (default ~/.it2/sessions)")
)

// Exit statuses. They are the point of running status from a loop, so they are
// part of the interface.
const (
	exitHealthy  = 0
	exitDegraded = 1 // warnings only
	exitBroken   = 2 // at least one error
	exitFailed   = 3 // cchooks itself failed
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cchooks [status] [flags]")
		flag.PrintDefaults()
	}

	// The subcommand is optional and comes first, but flag.Parse stops at the
	// first non-flag argument, so drop it before parsing rather than making
	// "cchooks status -json" silently ignore the flag.
	args := os.Args[1:]
	cmd := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "status" && args[0] != "store" {
			fmt.Fprintf(os.Stderr, "cchooks: unknown command %q\n", args[0])
			os.Exit(exitFailed)
		}
		cmd, args = args[0], args[1:]
	}
	flag.CommandLine.Parse(args)
	// Accept the subcommand on either side of the flags.
	if flag.NArg() == 1 && (flag.Arg(0) == "status" || flag.Arg(0) == "store") {
		cmd = flag.Arg(0)
		flag.CommandLine.Parse(nil)
	}
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "cchooks: unexpected argument %q\n", flag.Arg(0))
		os.Exit(exitFailed)
	}

	if err := run(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "cchooks:", err)
		os.Exit(exitFailed)
	}
}

func run(cmd string) error {
	report, err := cchooks.Read(projectDirs())
	if err != nil {
		return err
	}
	handlers, diags := filterAgent(report, *agentFlag)

	// A hook that runs and a store that fills are two different claims, and
	// only the second one means the pipeline works. Check both by default:
	// the failure this reports went unnoticed for nine months precisely
	// because it lived behind a command nobody had reason to run.
	st, storeDiags := cchooks.ReadStore(*rootFlag)
	diags = append(diags, storeDiags...)
	if !*allFlag {
		diags = withoutInfo(diags)
		storeDiags = withoutInfo(storeDiags)
	}
	if cmd == "store" {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(struct {
				Store       cchooks.StoreStats   `json:"store"`
				Diagnostics []cchooks.Diagnostic `json:"diagnostics"`
			}{st, storeDiags}); err != nil {
				return err
			}
		} else {
			printStore(st, storeDiags)
		}
		return exitFor(storeDiags)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Handlers    []cchooks.Handler    `json:"handlers"`
			Diagnostics []cchooks.Diagnostic `json:"diagnostics"`
		}{handlers, diags}); err != nil {
			return err
		}
	} else {
		printTable(handlers, diags)
	}

	return exitFor(diags)
}

func exitFor(diags []cchooks.Diagnostic) error {
	switch cchooks.Worst(diags) {
	case "error":
		os.Exit(exitBroken)
	case "warn":
		os.Exit(exitDegraded)
	}
	os.Exit(exitHealthy)
	return nil
}

// printStore shows the counts narrowing stage by stage, so the reader can see
// which one falls to zero rather than being told only that the answer is none.
func printStore(st cchooks.StoreStats, diags []cchooks.Diagnostic) {
	fmt.Println("event store", shorten(st.Root))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, row := range []struct {
		label string
		n     int
	}{
		{"session directories", st.Dirs},
		{"  with an events file", st.Files},
		{"non-blank lines", st.Lines},
		{"  carrying both ids", st.Bound},
		{"pane bindings resolved", st.Intervals},
	} {
		fmt.Fprintf(w, "  %s\t%d\n", row.label, row.n)
	}
	if st.Twinned > 0 {
		fmt.Fprintf(w, "  ids with twinned dirs\t%d\n", st.Twinned)
	}
	w.Flush()

	if len(diags) > 0 {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, d := range diags {
			fmt.Fprintf(w, "%s\t%s\t%s\n", d.Severity, d.Code, d.Message)
		}
		w.Flush()
	}
}

func projectDirs() []string {
	if *dirFlag != "" {
		return strings.Split(*dirFlag, ",")
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return []string{wd}
}

func filterAgent(r *cchooks.Report, agent string) ([]cchooks.Handler, []cchooks.Diagnostic) {
	if agent == "" {
		return r.Handlers, r.Diagnostics
	}
	var h []cchooks.Handler
	for _, x := range r.Handlers {
		if x.Layer.Agent == agent {
			h = append(h, x)
		}
	}
	var d []cchooks.Diagnostic
	for _, x := range r.Diagnostics {
		if x.Layer.Agent == agent {
			d = append(d, x)
		}
	}
	return h, d
}

func withoutInfo(diags []cchooks.Diagnostic) []cchooks.Diagnostic {
	var out []cchooks.Diagnostic
	for _, d := range diags {
		if d.Severity != "info" {
			out = append(out, d)
		}
	}
	return out
}

func printTable(handlers []cchooks.Handler, diags []cchooks.Diagnostic) {
	if len(handlers) == 0 {
		fmt.Println("no hooks installed")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	agent := ""
	for _, h := range handlers {
		if h.Layer.Agent != agent {
			if agent != "" {
				fmt.Fprintln(w, "\t\t\t")
			}
			agent = h.Layer.Agent
			fmt.Fprintf(w, "%s\t\t\t\n", agent)
		}
		event := h.Event
		switch h.Layer.Origin {
		case cchooks.Parked:
			event = "PARKED " + event
		case cchooks.Unresolved:
			event = "DEAD " + event
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", event, orDash(h.Matcher), provenance(h.Layer), command(h))
	}
	w.Flush()

	if len(diags) > 0 {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, d := range diags {
			where := shorten(d.Layer.Path)
			if d.Handler != nil {
				where += " " + d.Handler.Event
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Severity, d.Code, where, d.Message)
		}
		w.Flush()
	}
}

func provenance(l cchooks.Layer) string {
	switch l.Origin {
	case cchooks.Plugin, cchooks.Unresolved:
		return "plugin:" + l.Name
	case cchooks.Managed:
		return "managed"
	default:
		s := l.Origin.String()
		if l.Scope != "" {
			s += ":" + l.Scope
		}
		return s
	}
}

func command(h cchooks.Handler) string {
	s := h.Command
	if len(h.Args) > 0 {
		s += " " + strings.Join(h.Args, " ")
	}
	s = shorten(s)
	if h.Timeout > 0 {
		s += fmt.Sprintf(" (%ds)", h.Timeout)
	}
	return s
}

// shorten replaces the home directory with ~ so paths fit on a line.
func shorten(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	return strings.ReplaceAll(s, home+string(filepath.Separator), "~"+string(filepath.Separator))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
