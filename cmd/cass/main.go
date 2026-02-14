package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cass: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	jsonOutput := flag.Bool("json", false, "output in JSON format")
	dbPath := flag.String("db", "", "path to SQLite database (default ~/.cache/cass/index.db)")
	verbose := flag.Bool("v", false, "verbose output")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cass <command> [args]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "commands:")
		fmt.Fprintln(os.Stderr, "  detect   detect installed AI coding agents")
		fmt.Fprintln(os.Stderr, "  index    index sessions from detected agents")
		fmt.Fprintln(os.Stderr, "  search   search indexed sessions")
		fmt.Fprintln(os.Stderr, "  links    show inter-session communication (it2 send-text/get-screen/get-buffer)")
		fmt.Fprintln(os.Stderr, "  map      show iTerm2 <-> Claude session ID mappings")
		fmt.Fprintln(os.Stderr, "  stats    show index statistics")
		os.Exit(1)
	}

	logLevel := slog.LevelWarn
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	ctx := context.Background()
	cmd := args[0]

	// Detect doesn't need the store.
	if cmd == "detect" {
		return runDetect(ctx, *jsonOutput, logger)
	}

	svc, err := service.New(service.Config{
		DBPath: *dbPath,
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	defer svc.Close()

	switch cmd {
	case "index":
		return runIndex(ctx, svc, args[1:], *jsonOutput)
	case "search":
		return runSearch(ctx, svc, args[1:], *jsonOutput)
	case "links":
		return runLinks(ctx, svc, args[1:], *jsonOutput)
	case "map":
		return runMap(ctx, svc, args[1:], *jsonOutput)
	case "stats":
		return runStats(ctx, svc, *jsonOutput)
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func runDetect(ctx context.Context, jsonOut bool, logger *slog.Logger) error {
	svc, err := service.New(service.Config{
		DBPath: ":memory:",
		Logger: logger,
	})
	if err != nil {
		return err
	}
	defer svc.Close()

	results, err := svc.Detect(ctx)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(results)
	}
	for _, r := range results {
		status := "not found"
		if r.Found {
			status = strings.Join(r.Paths, ", ")
		}
		fmt.Printf("%-15s %s\n", r.Agent, status)
	}
	return nil
}

func runIndex(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	force := fs.Bool("force", false, "rebuild index from scratch")
	fs.Parse(args)

	count, err := svc.Index(ctx, *force)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]int{"indexed": count})
	}
	fmt.Printf("indexed %d sessions\n", count)
	return nil
}

func runSearch(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	agent := fs.String("agent", "", "filter by agent slug")
	workspace := fs.String("workspace", "", "filter by workspace path")
	since := fs.Duration("since", 0, "sessions within duration (e.g. 12h, 24h, 168h)")
	after := fs.String("after", "", "sessions after date (RFC3339 or YYYY-MM-DD)")
	before := fs.String("before", "", "sessions before date (RFC3339 or YYYY-MM-DD)")
	limit := fs.Int("n", 20, "max results")
	resume := fs.Bool("resume", false, "output resume commands for each session")
	fs.Parse(args)

	query := strings.Join(fs.Args(), " ")

	req := cass.SearchRequest{
		Query: query,
		Limit: *limit,
		Filters: cass.Filters{
			Agent:     *agent,
			Workspace: *workspace,
		},
	}

	if *since > 0 {
		req.Filters.After = time.Now().Add(-*since)
	}
	if *after != "" {
		t, err := parseTime(*after)
		if err != nil {
			return fmt.Errorf("bad -after: %w", err)
		}
		req.Filters.After = t
	}
	if *before != "" {
		t, err := parseTime(*before)
		if err != nil {
			return fmt.Errorf("bad -before: %w", err)
		}
		req.Filters.Before = t
	}
	result, err := svc.Search(ctx, req)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	if len(result.Hits) == 0 {
		fmt.Println("no results")
		return nil
	}

	if *resume {
		return outputResume(result)
	}

	for _, h := range result.Hits {
		snippet := strings.ReplaceAll(h.Snippet, "\n", " ")
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		fmt.Printf("[%s] %s\n", h.Agent, h.Title)
		if snippet != "" {
			fmt.Printf("  %s\n", snippet)
		}
		if h.Workspace != "" {
			fmt.Printf("  workspace: %s\n", h.Workspace)
		}
		fmt.Println()
	}
	fmt.Printf("(%d results)\n", result.TotalCount)
	return nil
}

func runStats(ctx context.Context, svc *service.Service, jsonOut bool) error {
	stats, err := svc.Stats(ctx)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(stats)
	}
	for k, v := range stats {
		fmt.Printf("%-20s %v\n", k, v)
	}
	return nil
}

func runMap(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	var filter string
	if len(args) > 0 {
		filter = args[0]
	}

	mappings, err := svc.Mappings(ctx, filter)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(mappings)
	}

	if len(mappings) == 0 {
		fmt.Println("no session mappings found")
		return nil
	}

	fmt.Printf("%-10s  %-10s  %-20s  %s\n", "iTerm2", "CASS", "Workspace", "Title")
	fmt.Println(strings.Repeat("─", 90))
	for _, m := range mappings {
		title := m.Title
		if len(title) > 40 {
			title = title[:40] + "..."
		}
		ws := m.Workspace
		if len(ws) > 20 {
			ws = "..." + ws[len(ws)-17:]
		}
		fmt.Printf("%-10s  %-10s  %-20s  %s\n", short(m.ItermSession), short(m.CASSSession), ws, title)
	}
	fmt.Printf("\n(%d mappings)\n", len(mappings))
	return nil
}

func runLinks(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("links", flag.ExitOnError)
	dot := fs.Bool("dot", false, "output in Graphviz DOT format")
	since := fs.Duration("since", 0, "only links within duration (e.g. 12h)")
	fs.Parse(args)

	var sessionID string
	if fs.NArg() > 0 {
		sessionID = fs.Arg(0)
	}

	links, err := svc.Links(ctx, sessionID)
	if err != nil {
		return err
	}

	// Filter by time if requested.
	if *since > 0 {
		cutoff := time.Now().Add(-*since)
		var filtered []cass.SessionLink
		for _, l := range links {
			if l.Timestamp != "" {
				t, err := time.Parse("2006-01-02T15:04:05Z07:00", l.Timestamp)
				if err == nil && t.Before(cutoff) {
					continue
				}
			}
			filtered = append(filtered, l)
		}
		links = filtered
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(links)
	}

	if len(links) == 0 {
		fmt.Println("no inter-session links found")
		return nil
	}

	if *dot {
		return outputDot(links)
	}

	return outputASCII(links)
}

func outputDot(links []cass.SessionLink) error {
	fmt.Println("digraph sessions {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [shape=box, style=rounded, fontname=\"monospace\"];")
	fmt.Println("  edge [fontname=\"monospace\", fontsize=10];")
	fmt.Println()

	// Collect unique nodes.
	nodes := make(map[string]bool)
	for _, l := range links {
		if l.SourceSession != "" {
			nodes[short(l.SourceSession)] = true
		}
		nodes[short(l.TargetSession)] = true
	}
	for n := range nodes {
		fmt.Printf("  %q;\n", n)
	}
	fmt.Println()

	// Edges.
	for _, l := range links {
		src := short(l.SourceSession)
		if src == "" {
			src = "?"
		}
		dst := short(l.TargetSession)
		style := ""
		switch l.Action {
		case "send-text":
			style = `[label="send", color=blue]`
		case "get-screen":
			style = `[label="screen", color=green, style=dashed]`
		case "get-buffer":
			style = `[label="buffer", color=orange, style=dashed]`
		}
		fmt.Printf("  %q -> %q %s;\n", src, dst, style)
	}
	fmt.Println("}")
	return nil
}

func outputASCII(links []cass.SessionLink) error {
	// Separate messages from observations.
	var messages, observations []cass.SessionLink
	for _, l := range links {
		switch l.Kind {
		case "observation":
			observations = append(observations, l)
		default:
			messages = append(messages, l)
		}
	}

	type edge struct {
		counts map[string]int
	}

	printGraph := func(title string, items []cass.SessionLink) {
		if len(items) == 0 {
			return
		}
		graph := make(map[string]map[string]*edge)
		nodes := make(map[string]bool)

		for _, l := range items {
			src := short(l.SourceSession)
			if src == "" {
				src = "?"
			}
			dst := short(l.TargetSession)
			nodes[src] = true
			nodes[dst] = true

			if graph[src] == nil {
				graph[src] = make(map[string]*edge)
			}
			if graph[src][dst] == nil {
				graph[src][dst] = &edge{counts: make(map[string]int)}
			}
			graph[src][dst].counts[l.Action]++
		}

		fmt.Printf("%s\n%s\n\n", title, strings.Repeat("─", len(title)))
		for src, targets := range graph {
			fmt.Printf("  [%s]\n", src)
			for dst, e := range targets {
				var parts []string
				for action, count := range e.counts {
					parts = append(parts, fmt.Sprintf("%dx %s", count, action))
				}
				arrow := "───>"
				if title[0] == 'O' { // Observations
					arrow = "···>"
				}
				fmt.Printf("    %s [%s]  (%s)\n", arrow, dst, strings.Join(parts, ", "))
			}
		}
		fmt.Printf("  %d nodes, %d links\n\n", len(nodes), len(items))
	}

	printGraph("Messages (send-text, send-key)", messages)
	printGraph("Observations (get-screen, get-buffer)", observations)

	return nil
}

func short(sid string) string {
	if len(sid) >= 8 {
		return sid[:8]
	}
	return sid
}

func outputResume(result *cass.SearchResult) error {
	for _, h := range result.Hits {
		title := h.Title
		if len(title) > 60 {
			title = title[:60] + "..."
		}
		fmt.Printf("# %s (%s)\n", title, h.StartedAt)
		if h.Workspace != "" {
			fmt.Printf("cd %s && claude --resume\n", h.Workspace)
		} else if h.SourcePath != "" {
			fmt.Printf("claude --resume  # source: %s\n", h.SourcePath)
		}
		fmt.Println()
	}
	return nil
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format: %s", s)
}

