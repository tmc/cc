package main

import (
	"bufio"
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
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/cass/web"
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
		fmt.Fprintln(os.Stderr, "  resume   search and resume a session interactively")
		fmt.Fprintln(os.Stderr, "  links    show inter-session communication graph")
		fmt.Fprintln(os.Stderr, "  map      show iTerm2 <-> Claude session ID mappings")
		fmt.Fprintln(os.Stderr, "  stats    show index statistics")
		fmt.Fprintln(os.Stderr, "  web      start web UI server")
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
	case "resume":
		return runResume(ctx, svc, args[1:])
	case "links":
		return runLinks(ctx, svc, args[1:], *jsonOutput)
	case "map":
		return runMap(ctx, svc, args[1:], *jsonOutput)
	case "stats":
		return runStats(ctx, svc, *jsonOutput)
	case "web":
		return runWeb(ctx, svc, args[1:], logger)
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

func buildSearchRequest(args []string) (cass.SearchRequest, error) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	agent := fs.String("agent", "", "filter by agent slug")
	workspace := fs.String("workspace", "", "filter by workspace path or project name")
	since := fs.Duration("since", 0, "sessions within duration (e.g. 12h, 24h, 168h)")
	after := fs.String("after", "", "sessions after date (RFC3339 or YYYY-MM-DD)")
	before := fs.String("before", "", "sessions before date (RFC3339 or YYYY-MM-DD)")
	limit := fs.Int("n", 20, "max results")
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
			return req, fmt.Errorf("bad -after: %w", err)
		}
		req.Filters.After = t
	}
	if *before != "" {
		t, err := parseTime(*before)
		if err != nil {
			return req, fmt.Errorf("bad -before: %w", err)
		}
		req.Filters.Before = t
	}
	return req, nil
}

func runSearch(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	// Parse -resume separately from the shared flags.
	var resume bool
	var filtered []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-resume" || args[i] == "--resume" {
			resume = true
		} else {
			filtered = append(filtered, args[i])
		}
	}

	req, err := buildSearchRequest(filtered)
	if err != nil {
		return err
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

	if resume {
		return outputResume(result)
	}

	for i, h := range result.Hits {
		printHit(i+1, h)
	}
	fmt.Println(countStyle.Render(fmt.Sprintf("(%d results)", result.TotalCount)))
	return nil
}

// runResume searches sessions and lets the user pick one to resume interactively.
func runResume(ctx context.Context, svc *service.Service, args []string) error {
	req, err := buildSearchRequest(args)
	if err != nil {
		return err
	}
	if req.Limit > 20 {
		req.Limit = 20
	}

	result, err := svc.Search(ctx, req)
	if err != nil {
		return err
	}

	if len(result.Hits) == 0 {
		fmt.Println("no results")
		return nil
	}

	// Display numbered results.
	for i, h := range result.Hits {
		printHit(i+1, h)
	}
	fmt.Println(countStyle.Render(fmt.Sprintf("(%d results)", result.TotalCount)))
	fmt.Println()

	// Prompt for selection.
	fmt.Print(titleStyle.Render("select session") + " [1-" + fmt.Sprint(len(result.Hits)) + "]: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return nil
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" || input == "q" {
		return nil
	}

	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 1 || idx > len(result.Hits) {
		return fmt.Errorf("invalid selection: %s", input)
	}

	h := result.Hits[idx-1]
	if h.Workspace == "" {
		return fmt.Errorf("no workspace for session %s", h.SessionID)
	}

	// Output the resume command for the user to execute.
	cmd := fmt.Sprintf("cd %s && claude --resume", h.Workspace)
	fmt.Println()
	fmt.Println(titleStyle.Render("run:"))
	fmt.Println(cmd)
	return nil
}

func printHit(num int, h cass.Hit) {
	project := shortProject(h.Workspace)
	when := relativeTime(h.StartedAt)

	// Line 1: number, agent, title.
	fmt.Printf("%s %s %s",
		numberStyle.Render(fmt.Sprintf("%2d.", num)),
		agentStyle.Render(fmt.Sprintf("[%s]", h.Agent)),
		titleStyle.Render(h.Title),
	)
	fmt.Println()

	// Line 2: snippet.
	if h.Snippet != "" {
		fmt.Printf("    %s\n", formatSnippet(h.Snippet))
	}

	// Line 3: project, time, and stats.
	var meta []string
	if project != "" {
		meta = append(meta, workspaceStyle.Render(project))
	}
	if when != "" {
		meta = append(meta, timeStyle.Render(when))
	}

	// Stats chips.
	var stats []string
	if h.ToolCalls > 0 {
		stats = append(stats, fmt.Sprintf("%d tools", h.ToolCalls))
	}
	if h.Turns > 1 {
		stats = append(stats, fmt.Sprintf("%d turns", h.Turns))
	}
	if h.FilesEdited > 0 {
		stats = append(stats, fmt.Sprintf("%d files", h.FilesEdited))
	}
	if h.LinesWritten > 0 {
		stats = append(stats, fmt.Sprintf("%d lines", h.LinesWritten))
	}
	totalTok := h.InputTokens + h.OutputTokens
	if totalTok >= 1000 {
		stats = append(stats, fmt.Sprintf("%dk tok", totalTok/1000))
	}
	if h.IT2Sends > 0 || h.IT2Screens > 0 || h.IT2Splits > 0 {
		var it2 []string
		if h.IT2Splits > 0 {
			it2 = append(it2, fmt.Sprintf("%d splits", h.IT2Splits))
		}
		if h.IT2Sends > 0 {
			it2 = append(it2, fmt.Sprintf("%d msgs", h.IT2Sends))
		}
		if h.IT2Screens > 0 {
			it2 = append(it2, fmt.Sprintf("%d reads", h.IT2Screens))
		}
		stats = append(stats, "it2:"+strings.Join(it2, ","))
	}
	if len(stats) > 0 {
		meta = append(meta, labelStyle.Render(strings.Join(stats, " | ")))
	}
	if len(meta) > 0 {
		fmt.Printf("    %s\n", strings.Join(meta, "  "))
	}
	fmt.Println()
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

func runWeb(ctx context.Context, svc *service.Service, args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dev := fs.Bool("dev", false, "serve static files from disk (for development)")
	fs.Parse(args)

	srv := web.New(web.Config{
		Service: svc,
		Addr:    *addr,
		DevMode: *dev,
		Logger:  logger,
	})

	fmt.Fprintf(os.Stderr, "cass web → http://localhost%s\n", *addr)
	return srv.Start(ctx)
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

	fmt.Printf("%s  %s  %s  %s\n",
		headerStyle.Render(fmt.Sprintf("%-10s", "iTerm2")),
		headerStyle.Render(fmt.Sprintf("%-10s", "CASS")),
		headerStyle.Render(fmt.Sprintf("%-20s", "Project")),
		headerStyle.Render("Title"),
	)
	for _, m := range mappings {
		title := m.Title
		if len(title) > 40 {
			title = title[:40] + "..."
		}
		project := shortProject(m.Workspace)
		if len(project) > 20 {
			project = project[:20]
		}
		fmt.Printf("%-10s  %-10s  %s  %s\n",
			nodeStyle.Render(short(m.ItermSession)),
			snippetStyle.Render(short(m.CASSSession)),
			workspaceStyle.Render(fmt.Sprintf("%-20s", project)),
			title,
		)
	}
	fmt.Printf("\n%s\n", countStyle.Render(fmt.Sprintf("(%d mappings)", len(mappings))))
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
		return outputDotLabeled(ctx, svc, links)
	}

	return outputASCIILabeled(ctx, svc, links)
}

// collectNodeIDs gathers all unique short session IDs from links.
func collectNodeIDs(links []cass.SessionLink) []string {
	seen := make(map[string]bool)
	for _, l := range links {
		src := short(l.SourceSession)
		if src != "" {
			seen[src] = true
		}
		seen[short(l.TargetSession)] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// resolveNodeLabels builds a map from short session ID to "ID project" label.
func resolveNodeLabels(ctx context.Context, svc *service.Service, nodeIDs []string) map[string]string {
	labels := make(map[string]string, len(nodeIDs))
	for _, id := range nodeIDs {
		labels[id] = id // default: just the ID
	}

	resolved, err := svc.ResolveLabels(ctx, nodeIDs)
	if err != nil {
		return labels
	}
	for prefix, info := range resolved {
		project := shortProject(info.Workspace)
		if project != "" {
			labels[prefix] = prefix + " " + project
		}
	}
	return labels
}

func outputDotLabeled(ctx context.Context, svc *service.Service, links []cass.SessionLink) error {
	nodeIDs := collectNodeIDs(links)
	labels := resolveNodeLabels(ctx, svc, nodeIDs)

	fmt.Println("digraph sessions {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [shape=box, style=rounded, fontname=\"monospace\"];")
	fmt.Println("  edge [fontname=\"monospace\", fontsize=10];")
	fmt.Println()

	for _, id := range nodeIDs {
		fmt.Printf("  %q [label=%q];\n", id, labels[id])
	}
	fmt.Println()

	for _, l := range links {
		src := short(l.SourceSession)
		if src == "" {
			src = "?"
		}
		dst := short(l.TargetSession)
		style := ""
		switch l.Action {
		case "send-text", "send-key":
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

func outputASCIILabeled(ctx context.Context, svc *service.Service, links []cass.SessionLink) error {
	nodeIDs := collectNodeIDs(links)
	labels := resolveNodeLabels(ctx, svc, nodeIDs)

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

	printGraph := func(title string, items []cass.SessionLink, arrow string) {
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

		fmt.Printf("%s\n%s\n\n", headerStyle.Render(title), strings.Repeat("─", len(title)))
		for src, targets := range graph {
			fmt.Printf("  %s\n", nodeStyle.Render(fmt.Sprintf("[%s]", labels[src])))
			for dst, e := range targets {
				var parts []string
				for action, count := range e.counts {
					parts = append(parts, fmt.Sprintf("%dx %s", count, action))
				}
				arrowStyled := sendStyle.Render(arrow)
				if title[0] == 'O' {
					arrowStyled = observeStyle.Render(arrow)
				}
				fmt.Printf("    %s %s  %s\n",
					arrowStyled,
					nodeStyle.Render(fmt.Sprintf("[%s]", labels[dst])),
					labelStyle.Render(fmt.Sprintf("(%s)", strings.Join(parts, ", "))),
				)
			}
		}
		fmt.Printf("  %s\n\n", countStyle.Render(fmt.Sprintf("%d nodes, %d links", len(nodes), len(items))))
	}

	printGraph("Messages (send-text, send-key)", messages, "───>")
	printGraph("Observations (get-screen, get-buffer)", observations, "···>")

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

// timeSince returns the duration since t (used by format.go).
func timeSince(t time.Time) time.Duration {
	return time.Since(t)
}

// Ensure store.SessionMapping is referenced for go vet.
var _ = store.SessionMapping{}
