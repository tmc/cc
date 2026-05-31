package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	noColorFlag := flag.Bool("no-color", false, "disable colored output")
	flag.Parse()

	configureColor(*noColorFlag)

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cass <command> [args]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "commands:")
		fmt.Fprintln(os.Stderr, "  detect     detect installed AI coding agents")
		fmt.Fprintln(os.Stderr, "  index      index sessions from detected agents")
		fmt.Fprintln(os.Stderr, "  search     search indexed sessions")
		fmt.Fprintln(os.Stderr, "  resume     search and resume a session interactively")
		fmt.Fprintln(os.Stderr, "  goals      list goal-mode objectives")
		fmt.Fprintln(os.Stderr, "  skills     list skill usage")
		fmt.Fprintln(os.Stderr, "  links      show inter-session communication graph")
		fmt.Fprintln(os.Stderr, "  map        show iTerm2 <-> Claude session ID mappings")
		fmt.Fprintln(os.Stderr, "  stats      show index statistics")
		fmt.Fprintln(os.Stderr, "  subagents  list Task subagent runs")
		fmt.Fprintln(os.Stderr, "  workflows  list native workflow runs")
		fmt.Fprintln(os.Stderr, "  requests   show HAR-derived API request breakdown")
		fmt.Fprintln(os.Stderr, "  web        start web UI server")
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
	case "goals":
		return runGoals(ctx, svc, args[1:], *jsonOutput)
	case "skills":
		return runSkills(ctx, svc, args[1:], *jsonOutput)
	case "links":
		return runLinks(ctx, svc, args[1:], *jsonOutput)
	case "map":
		return runMap(ctx, svc, args[1:], *jsonOutput)
	case "stats":
		return runStats(ctx, svc, *jsonOutput)
	case "subagents":
		return runSubagents(ctx, svc, args[1:], *jsonOutput)
	case "workflows":
		return runWorkflows(ctx, svc, args[1:], *jsonOutput)
	case "requests":
		return runRequests(ctx, svc, args[1:], *jsonOutput)
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
	harDir := fs.String("har-dir", "", "directory of Proxyman HAR export files to ingest")
	sessionDir := fs.String("session-dir", "", "file or directory of .proxymansessionv2/.proxymanlogv2 files to ingest")
	artifactDirs := fs.Bool("artifact-dirs", true, "scan ~/.it2/sessions/*/proxy-traffic.*.jsonl (default on)")
	noArtifactDirs := fs.Bool("no-artifact-dirs", false, "disable artifact dir scanning")
	fs.Parse(args)

	var (
		count    int
		err      error
		targeted = fs.NArg() > 0
	)
	if targeted {
		count, err = svc.IndexRoots(ctx, fs.Args())
	} else {
		count, err = svc.Index(ctx, *force)
	}
	if err != nil {
		return err
	}

	harCount := 0
	if *harDir != "" {
		harCount, err = svc.IndexHAR(ctx, *harDir)
		if err != nil {
			return fmt.Errorf("index har: %w", err)
		}
	}

	sessionV2Count := 0
	if *sessionDir != "" {
		sessionV2Count, err = svc.IndexSessionV2(ctx, *sessionDir)
		if err != nil {
			return fmt.Errorf("index sessionv2: %w", err)
		}
	}

	artifactCount := 0
	if !targeted && *artifactDirs && !*noArtifactDirs {
		artifactCount, err = svc.IndexArtifactDirs(ctx, "")
		if err != nil {
			// Non-fatal: artifact dirs may not exist yet.
			fmt.Fprintf(os.Stderr, "cass: artifact dir scan: %v\n", err)
		}
	}

	// Non-fatal: team configs may not exist.
	teamConfigCount := 0
	jobCount := 0
	agentDefCount := 0
	if !targeted {
		teamConfigCount, _ = svc.IndexTeamConfigs(ctx, "")
		jobCount, _ = svc.IndexJobs(ctx, "")
		agentDefCount, _ = svc.IndexAgentDefs(ctx, "")
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"indexed":            count,
			"har_requests":       harCount,
			"sessionv2_requests": sessionV2Count,
			"artifact_requests":  artifactCount,
			"team_configs":       teamConfigCount,
			"jobs":               jobCount,
			"agent_defs":         agentDefCount,
		})
	}
	fmt.Printf("indexed %d sessions\n", count)
	if harCount > 0 {
		fmt.Printf("indexed %d HAR requests\n", harCount)
	}
	if sessionV2Count > 0 {
		fmt.Printf("indexed %d sessionv2 requests\n", sessionV2Count)
	}
	if artifactCount > 0 {
		fmt.Printf("indexed %d artifact requests\n", artifactCount)
	}
	if teamConfigCount > 0 {
		fmt.Printf("indexed %d team configs\n", teamConfigCount)
	}
	if jobCount > 0 {
		fmt.Printf("indexed %d jobs\n", jobCount)
	}
	if agentDefCount > 0 {
		fmt.Printf("indexed %d agent defs\n", agentDefCount)
	}
	return nil
}

func buildSearchRequest(args []string) (cass.SearchRequest, error) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	agent := fs.String("agent", "", "filter by agent slug")
	workspace := fs.String("workspace", "", "filter by workspace path or project name")
	gitCommonDir := fs.String("git-common-dir", "", "filter by resolved git common dir (stable across worktrees)")
	goalStatus := fs.String("goal-status", "", "filter by goal status")
	skill := fs.String("skill", "", "filter by skill name or path substring")
	pwd := fs.Bool("pwd", false, "filter to current working directory")
	since := fs.Duration("since", 0, "sessions within duration (e.g. 12h, 24h, 168h)")
	after := fs.String("after", "", "sessions after date (RFC3339 or YYYY-MM-DD)")
	before := fs.String("before", "", "sessions before date (RFC3339 or YYYY-MM-DD)")
	sort := fs.String("sort", "", "sort order: recent, relevance, started, oldest")
	limit := fs.Int("n", 20, "max results")
	fs.Parse(args)

	query := strings.Join(fs.Args(), " ")

	ws := *workspace
	if *pwd && ws == "" {
		if wd, err := os.Getwd(); err == nil {
			if rp, err := filepath.EvalSymlinks(wd); err == nil {
				wd = rp
			}
			// The workspace path stored in the DB uses dash-encoded paths
			// where dots in domain names become slashes (github.com → github/com).
			// Match using the last path components to avoid this mismatch.
			ws = filepath.Base(filepath.Dir(wd)) + "/" + filepath.Base(wd)
		}
	}

	req := cass.SearchRequest{
		Query: query,
		Sort:  cass.SortMode(*sort),
		Limit: *limit,
		Filters: cass.Filters{
			Agent:        *agent,
			Workspace:    ws,
			GitCommonDir: *gitCommonDir,
			GoalStatus:   *goalStatus,
			Skill:        *skill,
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
	cmd := resumeCommand(h)
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
	if len(h.Goals) > 0 {
		goal := h.Goals[len(h.Goals)-1]
		obj := strings.Join(strings.Fields(goal.Objective), " ")
		if len(obj) > 100 {
			obj = obj[:97] + "..."
		}
		status := goal.EffectiveStatus
		if status == "" {
			status = cass.GoalEffectiveStatus(goal)
		}
		fmt.Printf("    %s %s\n", statusStyle(status), obj)
	}
	if len(h.Skills) > 0 {
		var names []string
		for _, sk := range h.Skills {
			if sk.Kind == "available" {
				continue
			}
			names = append(names, sk.Name)
			if len(names) == 4 {
				break
			}
		}
		if len(names) == 0 {
			for _, sk := range h.Skills {
				names = append(names, sk.Name)
				if len(names) == 4 {
					break
				}
			}
		}
		if len(names) > 0 {
			fmt.Printf("    %s %s\n", statusStyle("skill"), strings.Join(names, ", "))
		}
	}

	// Line 3: project, time, duration, and stats.
	var meta []string
	if project != "" {
		meta = append(meta, workspaceStyle.Render(project))
	}
	if when != "" {
		lastActivity := relativeTime(h.EndedAt)
		if lastActivity != "" && lastActivity != when {
			meta = append(meta, timeStyle.Render(when+" → "+lastActivity))
		} else {
			meta = append(meta, timeStyle.Render(when))
		}
	}
	if dur := formatDuration(h.DurationSecs); dur != "" {
		meta = append(meta, durationStyle.Render(dur))
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
	if h.SubagentEntries > 0 {
		stats = append(stats, fmt.Sprintf("mirrors %d/%d", h.SubagentMirroredEntries, h.SubagentEntries))
	}

	if h.InputTokens > 0 || h.OutputTokens > 0 || h.CacheReads > 0 || h.CacheCreationInputTokens > 0 {
		var toks []string
		if h.InputTokens > 0 {
			toks = append(toks, fmt.Sprintf("in:%s", formatTokens(h.InputTokens)))
		}
		if h.OutputTokens > 0 {
			toks = append(toks, fmt.Sprintf("out:%s", formatTokens(h.OutputTokens)))
		}
		if h.CacheReads > 0 {
			toks = append(toks, fmt.Sprintf("cache_r:%s", formatTokens(h.CacheReads)))
		}
		if h.CacheCreationInputTokens > 0 {
			toks = append(toks, fmt.Sprintf("cache_w:%s", formatTokens(h.CacheCreationInputTokens)))
		}
		stats = append(stats, strings.Join(toks, " "))
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
	if h.Sparkline != "" {
		stats = append(stats, h.Sparkline)
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

	// Include HAR request count if any are indexed.
	reqCount, err := svc.APIRequestCount(ctx)
	if err == nil && reqCount > 0 {
		stats["har_request_count"] = reqCount
	}

	// Include aggregate token stats.
	agg, err := svc.AggregateStats(ctx, time.Time{}, time.Time{})
	if err == nil {
		for k, v := range agg {
			stats[k] = v
		}
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(stats)
	}

	// Print core counts first.
	for _, key := range []string{"session_count", "har_request_count", "last_indexed"} {
		if v, ok := stats[key]; ok {
			fmt.Printf("%-28s %v\n", key, v)
		}
	}

	// Print token stats with notes.
	if v, ok := stats["input_tokens"]; ok {
		fmt.Printf("%-28s %v\n", "input_tokens", v)
	}
	if v, ok := stats["output_tokens"]; ok {
		fmt.Printf("%-28s %v  (undercounted: JSONL stores streaming-start snapshot)\n", "output_tokens", v)
	}
	if v, ok := stats["cache_creation_input_tokens"]; ok {
		fmt.Printf("%-28s %v  (billed at 1.25x)\n", "cache_creation_input_tokens", v)
	}
	if v, ok := stats["cache_read_input_tokens"]; ok {
		fmt.Printf("%-28s %v\n", "cache_read_input_tokens", v)
	}

	// Print remaining keys.
	skip := map[string]bool{
		"session_count": true, "har_request_count": true, "last_indexed": true,
		"input_tokens": true, "output_tokens": true,
		"cache_creation_input_tokens": true, "cache_read_input_tokens": true,
	}
	for k, v := range stats {
		if !skip[k] {
			fmt.Printf("%-28s %v\n", k, v)
		}
	}

	// Subagent runs summary.
	if sum, err := svc.SubagentRunsSummary(ctx); err == nil && sum.TotalRuns > 0 {
		fmt.Printf("%-28s %d runs across %d sessions\n", "subagent_runs", sum.TotalRuns, sum.SessionsWithRuns)
		if sum.TotalTokens > 0 {
			fmt.Printf("%-28s %d\n", "subagent_total_tokens", sum.TotalTokens)
		}
		if sum.TotalDurationMs > 0 {
			fmt.Printf("%-28s %s\n", "subagent_total_duration", time.Duration(sum.TotalDurationMs)*time.Millisecond)
		}
	}
	return nil
}

func runGoals(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("goals", flag.ExitOnError)
	status := fs.String("status", "", "filter by goal status")
	limit := fs.Int("limit", 50, "max rows to return")
	if err := fs.Parse(args); err != nil {
		return err
	}

	goals, err := svc.Goals(ctx, *status, *limit)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(goals)
	}
	if len(goals) == 0 {
		fmt.Println("no goals")
		return nil
	}
	for _, g := range goals {
		when := relativeTime(g.EndedAt)
		status := g.EffectiveStatus
		if status == "" {
			status = cass.GoalEffectiveStatus(g.Goal)
		}
		obj := strings.Join(strings.Fields(g.Objective), " ")
		if len(obj) > 90 {
			obj = obj[:87] + "..."
		}
		meta := shortProject(g.Workspace)
		if g.TimeUsedSeconds > 0 {
			if meta != "" {
				meta += "  "
			}
			meta += formatDuration(g.TimeUsedSeconds)
		}
		if g.TokensUsed > 0 {
			if meta != "" {
				meta += "  "
			}
			meta += formatTokens(g.TokensUsed) + " tok"
		}
		if n := cass.GoalUnresolvedGateCount(g.Goal); n > 0 {
			if meta != "" {
				meta += "  "
			}
			meta += fmt.Sprintf("%d blocked gates", n)
		}
		if when != "" {
			if meta != "" {
				meta += "  "
			}
			meta += when
		}
		fmt.Printf("%-10s  %-10s  %s\n", statusStyle(status), short(g.SessionID), obj)
		if meta != "" {
			fmt.Printf("                        %s\n", meta)
		}
	}
	return nil
}

func runSkills(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("skills", flag.ExitOnError)
	skill := fs.String("skill", "", "filter by skill name or path substring")
	kind := fs.String("kind", "", "filter by signal kind")
	limit := fs.Int("limit", 50, "max rows to return")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 && *skill == "" {
		*skill = fs.Arg(0)
	}

	skills, err := svc.Skills(ctx, *skill, *kind, *limit)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(skills)
	}
	if len(skills) == 0 {
		fmt.Println("no skills")
		return nil
	}
	for _, sk := range skills {
		when := relativeTime(sk.EndedAt)
		name := sk.Name
		if sk.Path != "" {
			name += "  " + sk.Path
		}
		meta := shortProject(sk.Workspace)
		if sk.Count > 1 {
			if meta != "" {
				meta += "  "
			}
			meta += fmt.Sprintf("%d signals", sk.Count)
		}
		if len(sk.Evidence) > 0 {
			if meta != "" {
				meta += "  "
			}
			meta += strings.Join(sk.Evidence, "; ")
		}
		if when != "" {
			if meta != "" {
				meta += "  "
			}
			meta += when
		}
		fmt.Printf("%-10s  %-10s  %s\n", statusStyle(sk.Kind), short(sk.SessionID), name)
		if meta != "" {
			fmt.Printf("                        %s\n", meta)
		}
	}
	return nil
}

func statusStyle(status string) string {
	switch status {
	case "complete", "completed":
		return labelStyle.Render("complete")
	case "active":
		return titleStyle.Render("active")
	case "selected":
		return titleStyle.Render("selected")
	case "loaded", "expanded":
		return labelStyle.Render(status)
	case "available":
		return snippetStyle.Render("available")
	case "skill":
		return labelStyle.Render("skill")
	default:
		return snippetStyle.Render(status)
	}
}

func runSubagents(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("subagents", flag.ExitOnError)
	workspace := fs.String("workspace", "", "filter by workspace path substring")
	gitCommonDir := fs.String("git-common-dir", "", "filter by resolved git common dir")
	model := fs.String("model", "", "filter by model id (exact match)")
	agentType := fs.String("agent-type", "", "filter by agent type (e.g. general-purpose)")
	limit := fs.Int("limit", 50, "max rows to return")
	byType := fs.Bool("by-agent-type", false, "show count histogram by agent type instead of listing runs")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *byType {
		sum, err := svc.SubagentRunsSummary(ctx)
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(os.Stdout).Encode(sum.ByAgentType)
		}
		// Stable order: most-frequent first.
		var rows []agentTypeCount
		for k, v := range sum.ByAgentType {
			rows = append(rows, agentTypeCount{k, v})
		}
		sortByCountDesc(rows)
		for _, r := range rows {
			fmt.Printf("%6d  %s\n", r.v, r.k)
		}
		return nil
	}

	runs, err := svc.SubagentRuns(ctx, store.SubagentRunFilter{
		Workspace:    *workspace,
		GitCommonDir: *gitCommonDir,
		Model:        *model,
		AgentType:    *agentType,
		Limit:        *limit,
	})
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(runs)
	}
	if len(runs) == 0 {
		return nil
	}
	for _, r := range runs {
		started := "-"
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.Format("2006-01-02 15:04")
		}
		dur := "-"
		if r.DurationMs > 0 {
			dur = (time.Duration(r.DurationMs) * time.Millisecond).Round(time.Millisecond).String()
		}
		title := r.ParentTitle
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Printf("%s  %-10s  %-15s  %-22s  %8s  %7d tok  %-9s  %s\n",
			started, r.AgentID, agentTypeOrDash(r.AgentType), modelOrDash(r.Model),
			dur, r.TotalTokens, statusOrDash(r.Status), title)
	}
	return nil
}

func agentTypeOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func modelOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func statusOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type workflowListEntry struct {
	ParentSessionID   string               `json:"parent_session_id"`
	RunID             string               `json:"run_id"`
	TaskID            string               `json:"task_id,omitempty"`
	Name              string               `json:"name,omitempty"`
	Description       string               `json:"description,omitempty"`
	Status            string               `json:"status,omitempty"`
	Summary           string               `json:"summary,omitempty"`
	ScriptPath        string               `json:"script_path,omitempty"`
	TranscriptDir     string               `json:"transcript_dir,omitempty"`
	SourcePath        string               `json:"source_path,omitempty"`
	AgentCount        int                  `json:"agent_count,omitempty"`
	JournalEventCount int                  `json:"journal_event_count,omitempty"`
	StartedAt         int64                `json:"started_at,omitempty"`
	CompletedAt       int64                `json:"completed_at,omitempty"`
	Phases            []cass.WorkflowPhase `json:"phases,omitempty"`
	Agents            []cass.WorkflowAgent `json:"agents,omitempty"`
}

func workflowEntryFromRow(r store.WorkflowRow) workflowListEntry {
	e := workflowListEntry{
		ParentSessionID:   r.ParentSessionID,
		RunID:             r.RunID,
		TaskID:            r.TaskID,
		Name:              r.Name,
		Description:       r.Description,
		Status:            r.Status,
		Summary:           r.Summary,
		ScriptPath:        r.ScriptPath,
		TranscriptDir:     r.TranscriptDir,
		SourcePath:        r.SourcePath,
		AgentCount:        r.AgentCount,
		JournalEventCount: r.JournalEventCount,
		StartedAt:         positiveUnix(r.StartedAt),
		CompletedAt:       positiveUnix(r.CompletedAt),
	}
	if r.PhasesJSON != "" && r.PhasesJSON != "[]" {
		_ = json.Unmarshal([]byte(r.PhasesJSON), &e.Phases)
	}
	if r.AgentsJSON != "" && r.AgentsJSON != "[]" {
		_ = json.Unmarshal([]byte(r.AgentsJSON), &e.Agents)
	}
	return e
}

func positiveUnix(v int64) int64 {
	if v > 0 {
		return v
	}
	return 0
}

func runWorkflows(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("workflows", flag.ExitOnError)
	sessionID := fs.String("session", "", "filter by parent session id")
	since := fs.Duration("since", 0, "only workflows started within duration (e.g. 24h, 168h)")
	status := fs.String("status", "", "filter by workflow status")
	limit := fs.Int("limit", 50, "max rows to return")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 && *sessionID == "" {
		*sessionID = fs.Arg(0)
	}
	var cutoff int64
	if *since > 0 {
		cutoff = time.Now().Add(-*since).Unix()
	}

	var rows []store.WorkflowRow
	var err error
	if *sessionID == "" && cutoff > 0 {
		rows, err = svc.WorkflowsSince(ctx, time.Unix(cutoff, 0))
	} else {
		rows, err = svc.Workflows(ctx, *sessionID)
	}
	if err != nil {
		return err
	}
	workflows := []workflowListEntry{}
	for _, row := range rows {
		if cutoff > 0 && positiveUnix(row.StartedAt) < cutoff {
			continue
		}
		w := workflowEntryFromRow(row)
		if *status != "" && w.Status != *status {
			continue
		}
		workflows = append(workflows, w)
		if *limit > 0 && len(workflows) >= *limit {
			break
		}
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(workflows)
	}
	if len(workflows) == 0 {
		fmt.Println("no workflows")
		return nil
	}
	for _, w := range workflows {
		started := "-"
		if w.StartedAt > 0 {
			started = time.Unix(w.StartedAt, 0).Format("2006-01-02 15:04")
		}
		name := firstNonEmpty(w.Name, w.RunID, "workflow")
		agentCount := w.AgentCount
		if len(w.Agents) > agentCount {
			agentCount = len(w.Agents)
		}
		meta := fmt.Sprintf("%d phases  %d agents", len(w.Phases), agentCount)
		if w.JournalEventCount > 0 {
			meta += fmt.Sprintf("  %d events", w.JournalEventCount)
		}
		fmt.Printf("%s  %-8s  %-12s  %-28s  %s\n",
			started, short(w.ParentSessionID), statusOrDash(w.Status), trimText(name, 28), meta)
		if w.Description != "" {
			fmt.Printf("                                  %s\n", trimText(strings.Join(strings.Fields(w.Description), " "), 90))
		}
		if phases := workflowPhaseTitles(w.Phases); len(phases) > 0 {
			fmt.Printf("                                  phases: %s\n", strings.Join(phases, " / "))
		}
		if agents := workflowAgentLabels(w.Agents); len(agents) > 0 {
			fmt.Printf("                                  agents: %s\n", strings.Join(agents, ", "))
		}
	}
	return nil
}

func workflowPhaseTitles(phases []cass.WorkflowPhase) []string {
	var out []string
	for _, p := range phases {
		if p.Title != "" {
			out = append(out, p.Title)
		}
	}
	return out
}

func workflowAgentLabels(agents []cass.WorkflowAgent) []string {
	var out []string
	for i, a := range agents {
		name := a.Label
		if name == "" && a.Title != "" && !looksLikeWorkflowPromptTitle(a.Title) {
			name = a.Title
		}
		name = firstNonEmpty(name, a.AgentType, a.ID)
		if name == "" {
			name = numberedAgentName(i)
		}
		if len(out) == 5 {
			out = append(out, fmt.Sprintf("+%d more", len(agents)-i))
			break
		}
		out = append(out, trimText(name, 28))
	}
	return out
}

func looksLikeWorkflowPromptTitle(title string) bool {
	t := strings.TrimSpace(title)
	return len(t) > 72 || strings.HasPrefix(strings.ToLower(t), "you are ") ||
		strings.Contains(t, "READ THESE FILES FIRST")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func numberedAgentName(i int) string {
	return "agent " + fmt.Sprintf("%02d", i+1)
}

func trimText(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

type agentTypeCount struct {
	k string
	v int
}

// sortByCountDesc sorts in descending count order, breaking ties on key.
func sortByCountDesc(rows []agentTypeCount) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			if b.v > a.v || (b.v == a.v && b.k < a.k) {
				rows[j-1], rows[j] = b, a
			} else {
				break
			}
		}
	}
}

func runWeb(ctx context.Context, svc *service.Service, args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dev := fs.Bool("dev", false, "serve static files from disk (for development)")
	verbose := fs.Bool("v", false, "log requests with timing info")
	pprofFlag := fs.Bool("pprof", false, "expose net/http/pprof under /debug/pprof/")
	fs.Parse(args)

	// When verbose, lower the log level so request logs are visible.
	if *verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	srv := web.New(web.Config{
		Service: svc,
		Addr:    *addr,
		DevMode: *dev,
		Verbose: *verbose,
		Pprof:   *pprofFlag,
		Logger:  logger,
	})

	// A bare ":port" addr binds all interfaces; show localhost so the printed
	// URL is clickable. An explicit host:port is already complete.
	host := *addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	fmt.Fprintf(os.Stderr, "cass web → http://%s\n", host)
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
			fmt.Println(resumeCommand(h))
		} else if h.SourcePath != "" {
			fmt.Printf("%s  # source: %s\n", resumeCommand(h), h.SourcePath)
		}
		fmt.Println()
	}
	return nil
}

func resumeCommand(h cass.Hit) string {
	prefix := ""
	if h.Workspace != "" {
		prefix = fmt.Sprintf("cd %s && ", shellQuote(h.Workspace))
	}

	switch h.Agent {
	case "gemini-cli":
		return prefix + "gemini --resume"
	case "codex-cli", "codex-app":
		if h.SessionID != "" {
			return prefix + "codex resume " + shellQuote(h.SessionID)
		}
		return prefix + "codex resume"
	default:
		if id := claudeResumeID(h); id != "" {
			return prefix + "claude --resume " + shellQuote(id)
		}
		return prefix + "claude --resume"
	}
}

func claudeResumeID(h cass.Hit) string {
	if h.SourcePath == "" || !strings.HasSuffix(h.SourcePath, ".jsonl") {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(h.SourcePath), ".jsonl")
	if strings.HasPrefix(base, "agent-") {
		return ""
	}
	return base
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-', c == '.', c == '/', c == ':', c == '@', c == '+', c == ',', c == '=':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runRequests shows HAR-derived API request breakdown, optionally filtered to a session.
func runRequests(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("requests", flag.ExitOnError)
	since := fs.Duration("since", 0, "requests within duration (e.g. 24h, 168h)")
	model := fs.String("model", "", "filter by model family (sonnet, haiku, opus)")
	purpose := fs.String("purpose", "", "filter by purpose (response, classifier, compact)")
	fs.Parse(args)

	var sessionID string
	if fs.NArg() > 0 {
		sessionID = fs.Arg(0)
	}

	filter := cass.APIRequestFilter{
		SessionID:   sessionID,
		ModelFamily: *model,
		Purpose:     *purpose,
	}
	if *since > 0 {
		filter.Since = time.Now().Add(-*since).Unix()
	}
	requests, err := svc.QueryRequestsFiltered(ctx, filter)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(requests)
	}

	if len(requests) == 0 {
		if sessionID != "" {
			fmt.Printf("no HAR requests indexed for session %s\n", sessionID)
		} else {
			fmt.Println("no HAR requests indexed (use: cass index --har-dir <path>)")
		}
		return nil
	}

	// Aggregate totals.
	var totIn, totOut, totCacheCreate, totCacheRead int
	modelCounts := make(map[string]int)
	purposeCounts := make(map[string]int)
	skillCounts := make(map[string]int)
	var builtinBytes, mcpBytes, skillBytes, systemBytes, convBytes int

	for _, r := range requests {
		totIn += r.InputTokens
		totOut += r.OutputTokens
		totCacheCreate += r.CacheCreationTokens
		totCacheRead += r.CacheReadTokens
		modelCounts[r.ModelFamily]++
		purposeCounts[r.Purpose]++
		if r.Breakdown != nil {
			builtinBytes += r.Breakdown.BuiltinToolBytes
			mcpBytes += r.Breakdown.MCPToolBytes
			skillBytes += r.Breakdown.SkillToolBytes
			systemBytes += r.Breakdown.SystemPromptBytes
			convBytes += r.Breakdown.ConversationBytes
			for _, name := range r.Breakdown.SkillNames {
				skillCounts[name]++
			}
		}
	}

	// Header.
	if sessionID != "" {
		fmt.Printf("%s  %s\n\n",
			headerStyle.Render("API Requests"),
			countStyle.Render(fmt.Sprintf("session %s", short(sessionID))),
		)
	} else {
		fmt.Printf("%s\n\n", headerStyle.Render("API Requests (all sessions)"))
	}

	// Per-request table.
	fmt.Printf("  %s  %-8s  %-10s  %8s  %8s  %8s\n",
		headerStyle.Render(fmt.Sprintf("%-19s", "time")),
		headerStyle.Render("model"),
		headerStyle.Render("purpose"),
		headerStyle.Render("in tok"),
		headerStyle.Render("out tok"),
		headerStyle.Render("cc tok"),
	)
	for _, r := range requests {
		ts := ""
		if r.Timestamp > 0 {
			ts = time.Unix(r.Timestamp, 0).Format("2006-01-02 15:04:05")
		}
		fmt.Printf("  %-19s  %-8s  %-10s  %8d  %8d  %8d\n",
			timeStyle.Render(ts),
			agentStyle.Render(r.ModelFamily),
			labelStyle.Render(r.Purpose),
			r.InputTokens,
			r.OutputTokens,
			r.CacheCreationTokens,
		)
	}

	// Summary totals.
	fmt.Printf("\n%s\n", strings.Repeat("─", 72))
	fmt.Printf("  %-27s  %8d  %8d  %8d\n",
		titleStyle.Render(fmt.Sprintf("TOTAL (%d requests)", len(requests))),
		totIn, totOut, totCacheCreate,
	)
	fmt.Printf("  %s\n", countStyle.Render("cache_read: "+fmt.Sprintf("%d", totCacheRead)))
	fmt.Printf("  %s\n", countStyle.Render("(output_tokens are final SSE values, accurate)"))

	// Model breakdown.
	fmt.Printf("\n%s\n", headerStyle.Render("By model"))
	for fam, n := range modelCounts {
		fmt.Printf("  %-10s %d requests\n", agentStyle.Render(fam), n)
	}

	// Purpose breakdown.
	fmt.Printf("\n%s\n", headerStyle.Render("By purpose"))
	for p, n := range purposeCounts {
		fmt.Printf("  %-12s %d requests\n", labelStyle.Render(p), n)
	}
	if len(skillCounts) > 0 {
		fmt.Printf("\n%s\n", headerStyle.Render("By skill"))
		var rows []agentTypeCount
		for k, v := range skillCounts {
			rows = append(rows, agentTypeCount{k, v})
		}
		sortByCountDesc(rows)
		for _, r := range rows {
			fmt.Printf("  %-28s %d requests\n", labelStyle.Render(r.k), r.v)
		}
	}

	// Context composition (only if breakdown data was parsed).
	totalContextBytes := builtinBytes + mcpBytes + skillBytes + systemBytes + convBytes
	if totalContextBytes > 0 {
		fmt.Printf("\n%s\n", headerStyle.Render("Context composition (avg across requests with breakdown)"))
		pct := func(n int) string {
			if totalContextBytes == 0 {
				return "0%"
			}
			return fmt.Sprintf("%d%%", n*100/totalContextBytes)
		}
		fmt.Printf("  %-28s %6s  (%s)\n", "tools: built-in", formatBytes(builtinBytes), pct(builtinBytes))
		fmt.Printf("  %-28s %6s  (%s)\n", "tools: MCP", formatBytes(mcpBytes), pct(mcpBytes))
		if skillBytes > 0 {
			fmt.Printf("  %-28s %6s  (%s)\n", "tools: skill", formatBytes(skillBytes), pct(skillBytes))
		}
		fmt.Printf("  %-28s %6s  (%s)\n", "system prompt", formatBytes(systemBytes), pct(systemBytes))
		fmt.Printf("  %-28s %6s  (%s)\n", "conversation", formatBytes(convBytes), pct(convBytes))
	}

	fmt.Println()
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
