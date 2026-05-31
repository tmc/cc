package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

type requestRecord struct {
	SessionID      string
	Timestamp      time.Time
	Model          string
	IsSidechain    bool
	InputTokens    int
	OutputTokens   int
	CacheRead      int
	CacheCreate    int
	ToolUseCount   int
	ContextSize    int
	Weight         float64
	ParallelActive int
	CompactFollow  bool
}

type pricingConfig struct {
	Default *pricing           `json:"default,omitempty"`
	Models  map[string]pricing `json:"models,omitempty"`
}

type sessionAgg struct {
	FirstTS        time.Time
	LastTS         time.Time
	RequestCount   int
	SidechainCount int
	ToolUseCount   int
	HasLoop        bool
}

type characteristicValue struct {
	Pct         float64 `json:"pct"`
	Weight      float64 `json:"weight"`
	Description string  `json:"description"`
	Callback    string  `json:"callback"`
}

type characteristicsWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Label string    `json:"label"`
}

type characteristicsTotals struct {
	Requests int     `json:"requests"`
	Sessions int     `json:"sessions"`
	Weight   float64 `json:"weight"`
}

type characteristicsReport struct {
	Window          characteristicsWindow          `json:"window"`
	Unit            string                         `json:"unit"`
	Totals          characteristicsTotals          `json:"totals"`
	Characteristics map[string]characteristicValue `json:"characteristics"`
	ModelShare      map[string]float64             `json:"model_share,omitempty"`
	UnknownModels   []string                       `json:"-"`
}

type characteristicSpec struct {
	Key         string
	Label       string
	Description string
	Callback    string
	Verbose     bool
}

var characteristicSpecs = []characteristicSpec{
	{Key: "parallel4+", Label: "4+ sessions running in parallel", Description: "usage while 4+ sessions were active in the lookback window", Callback: "Queue work when you do not need every session at once."},
	{Key: "context150k+", Label: "context > 150k", Description: "usage at or above the context threshold", Callback: "Compact mid-task or clear when switching tasks."},
	{Key: "subagentHeavy", Label: "subagent-heavy sessions", Description: "usage from sessions where sidechain requests dominate", Callback: "Be deliberate about spawning subagents."},
	{Key: "longRunning8h+", Label: "sessions active for 8+ hours", Description: "usage from sessions that span the long-running threshold", Callback: "Long background sessions should be intentional."},
	{Key: "cacheMissHeavy", Label: "cache-miss heavy", Description: "usage from requests with a low cache-read ratio", Callback: "Keep prompts stable across turns and avoid unnecessary clears.", Verbose: true},
	{Key: "compactFollow", Label: "within 3 turns of a compact", Description: "usage from requests immediately after a compaction", Callback: "Compact less often; clear is cheaper when switching tasks.", Verbose: true},
	{Key: "outputHeavy", Label: "output > 50% of cache-adjusted input", Description: "usage from requests with output heavier than input", Callback: "Chunk generation or consider a cheaper model for output-heavy work.", Verbose: true},
	{Key: "toolSpamHeavy", Label: "sessions averaging >6 tools/turn", Description: "usage from sessions with many tool uses per assistant request", Callback: "High tool counts usually mean under-planned tasks.", Verbose: true},
	{Key: "backgroundLoopShare", Label: "background/loop-like sessions", Description: "usage from sessions that look like loops or scheduled workers", Callback: "Set a sunset on looping or scheduled sessions.", Verbose: true},
}

type modelShare struct {
	Weight float64
}

func runCharacteristics(files []string) error {
	pricingCfg, err := loadPricingConfig(*pricingFileFlag)
	if err != nil {
		return err
	}
	report, err := analyzeCharacteristics(files, pricingCfg)
	if err != nil {
		return err
	}
	if len(report.UnknownModels) > 0 {
		fmt.Fprintf(os.Stderr, "warning: unknown models: %s\n", strings.Join(report.UnknownModels, ", "))
	}
	if *formatFlag == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	renderCharacteristicsReport(report)
	return nil
}

func runWatch(files []string) error {
	ticker := time.NewTicker(watchRefreshInterval)
	defer ticker.Stop()

	for {
		if err := watchCycle(files); err != nil {
			return err
		}
		<-ticker.C
	}
}

func watchCycle(files []string) error {
	clearWatchScreen()
	fmt.Fprintf(os.Stdout, "ccstats watch · refreshing every %s (Ctrl-C to stop)\n\n", watchRefreshInterval)
	return runCharacteristics(files)
}

func analyzeCharacteristics(files []string, pricingCfg pricingConfig) (*characteristicsReport, error) {
	var requests []requestRecord
	sessions := make(map[string]*sessionAgg)
	compactBudgets := make(map[string]int)
	modelShares := make(map[string]float64)
	unknownModels := make(map[string]struct{})

	for _, path := range files {
		entries, err := cc.ReadFile(context.Background(), path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, err)
			continue
		}
		for _, e := range entries {
			if e.SessionID == "" {
				continue
			}
			agg := sessions[e.SessionID]
			if agg == nil {
				agg = &sessionAgg{}
				sessions[e.SessionID] = agg
			}
			if !e.Timestamp.IsZero() {
				if agg.FirstTS.IsZero() || e.Timestamp.Before(agg.FirstTS) {
					agg.FirstTS = e.Timestamp
				}
				if agg.LastTS.IsZero() || e.Timestamp.After(agg.LastTS) {
					agg.LastTS = e.Timestamp
				}
			}
			if e.Type == "system" && e.Subtype == "compact_boundary" {
				compactBudgets[e.SessionID] = 3
				continue
			}
			if e.CustomTitle != "" || e.Slug != "" {
				title := strings.ToLower(e.CustomTitle + " " + e.Slug)
				if strings.Contains(title, "/loop") {
					agg.HasLoop = true
				}
			}
			if e.Message == nil || e.Message.Role != "assistant" {
				continue
			}

			req := requestRecord{
				SessionID:    e.SessionID,
				Timestamp:    e.Timestamp,
				Model:        e.Message.Model,
				IsSidechain:  e.IsSidechain,
				ToolUseCount: len(e.Message.ToolUses()),
			}
			if usage := e.Message.Usage; usage != nil {
				req.InputTokens = usage.InputTokens
				req.OutputTokens = usage.OutputTokens
				req.CacheRead = usage.CacheReadInputTokens
				req.CacheCreate = usage.CacheCreationInputTokens
				req.ContextSize = req.InputTokens + req.CacheRead + req.CacheCreate
				req.Weight = requestWeight(req, pricingCfg)
			}
			if family := modelFamily(req.Model); family == req.Model && family != "" && family != "unknown" {
				unknownModels[req.Model] = struct{}{}
			}
			if compactBudgets[e.SessionID] > 0 {
				req.CompactFollow = true
				compactBudgets[e.SessionID]--
			}

			for _, tu := range e.Message.ToolUses() {
				name := strings.ToLower(tu.Name)
				if strings.Contains(name, "schedulewakeup") || strings.Contains(name, "croncreate") {
					agg.HasLoop = true
				}
			}

			agg.RequestCount++
			if req.IsSidechain {
				agg.SidechainCount++
			}
			agg.ToolUseCount += req.ToolUseCount
			requests = append(requests, req)
			modelShares[modelFamily(req.Model)] += req.Weight
		}
	}

	if len(requests) == 0 {
		return nil, fmt.Errorf("no API requests found")
	}

	sort.SliceStable(requests, func(i, j int) bool {
		if requests[i].Timestamp.Equal(requests[j].Timestamp) {
			if requests[i].SessionID == requests[j].SessionID {
				return requests[i].Model < requests[j].Model
			}
			return requests[i].SessionID < requests[j].SessionID
		}
		return requests[i].Timestamp.Before(requests[j].Timestamp)
	})

	parallelWindow := effectiveParallelWindow()
	var parallel ParallelIndex
	for _, req := range requests {
		parallel.Add(req.SessionID, req.Timestamp, parallelWindow)
	}
	for i := range requests {
		requests[i].ParallelActive = parallel.ActiveAt(requests[i].Timestamp)
	}

	carriers := make(map[string]float64)
	totalWeight := 0.0
	for i := range requests {
		req := requests[i]
		totalWeight += req.Weight
		session := sessions[req.SessionID]
		if session == nil || session.RequestCount == 0 {
			continue
		}
		sessionDuration := session.LastTS.Sub(session.FirstTS)
		subagentShare := float64(session.SidechainCount) / float64(session.RequestCount)
		toolShare := float64(session.ToolUseCount) / float64(session.RequestCount)
		cacheReadRatio := 0.0
		if req.ContextSize > 0 {
			cacheReadRatio = float64(req.CacheRead) / float64(req.ContextSize)
		}

		add := func(key string, enabled bool) {
			if enabled {
				carriers[key] += req.Weight
			}
		}

		add("parallel4+", req.ParallelActive >= 4)
		add("context150k+", req.ContextSize >= *contextThresholdFlag)
		add("subagentHeavy", subagentShare >= 0.30)
		add("longRunning8h+", sessionDuration >= *longRunningThresholdFlag)

		if *verboseFlag {
			add("cacheMissHeavy", cacheReadRatio < 0.40 && req.ContextSize > 0)
			add("compactFollow", req.CompactFollow)
			add("outputHeavy", req.OutputTokens*2 > req.ContextSize && req.ContextSize > 0)
			add("toolSpamHeavy", toolShare > 6.0)
			add("backgroundLoopShare", session.HasLoop)
		}
	}

	windowStart := requests[0].Timestamp
	windowEnd := requests[len(requests)-1].Timestamp
	if *sinceFlag != "" {
		windowEnd = windowEnd.UTC()
		windowStart = windowStart.UTC()
	}

	report := &characteristicsReport{
		Window: characteristicsWindow{
			Start: windowStart,
			End:   windowEnd,
			Label: *sinceFlag,
		},
		Unit: *unitFlag,
		Totals: characteristicsTotals{
			Requests: len(requests),
			Sessions: len(sessions),
			Weight:   totalWeight,
		},
		Characteristics: make(map[string]characteristicValue),
	}

	for _, spec := range characteristicSpecs {
		if spec.Verbose && !*verboseFlag {
			continue
		}
		weight := carriers[spec.Key]
		report.Characteristics[spec.Key] = characteristicValue{
			Pct:         pct(weight, totalWeight),
			Weight:      weight,
			Description: spec.Description,
			Callback:    spec.Callback,
		}
	}
	if *verboseFlag {
		report.ModelShare = make(map[string]float64)
		for name, weight := range modelShares {
			report.ModelShare[name] = pct(weight, totalWeight)
		}
	}
	if len(unknownModels) > 0 {
		report.UnknownModels = make([]string, 0, len(unknownModels))
		for name := range unknownModels {
			report.UnknownModels = append(report.UnknownModels, name)
		}
		sort.Strings(report.UnknownModels)
	}
	return report, nil
}

func renderCharacteristicsReport(report *characteristicsReport) {
	label := report.Window.Label
	if label == "" {
		label = fmt.Sprintf("%s → %s", report.Window.Start.Format("2006-01-02 15:04"), report.Window.End.Format("2006-01-02 15:04"))
	} else {
		label = "Last " + label
	}
	fmt.Println(label + " · these are independent characteristics of your usage, not a breakdown")
	fmt.Println()

	for _, spec := range characteristicSpecs {
		if spec.Verbose && !*verboseFlag {
			continue
		}
		val := report.Characteristics[spec.Key]
		fmt.Printf("%4.0f%%  %-36s [%s]\n", val.Pct*100, spec.Label, spec.Key)
	}

	fmt.Println()
	fmt.Printf("Unit: %s · Window: %s → %s\n",
		renderUnitLabel(report.Unit),
		report.Window.Start.Format("2006-01-02 15:04"),
		report.Window.End.Format("2006-01-02 15:04"),
	)
	fmt.Printf("Requests: %d · Sessions: %d · Total weight: %s tok-eq\n",
		report.Totals.Requests,
		report.Totals.Sessions,
		fmtTokens(int(math.Round(report.Totals.Weight))),
	)

	if *verboseFlag && len(report.ModelShare) > 0 {
		fmt.Println()
		fmt.Println("Model share:")
		keys := make([]string, 0, len(report.ModelShare))
		for k := range report.ModelShare {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pct := report.ModelShare[k]
			fmt.Printf("  %-8s %3.0f%%  %s\n", k, pct*100, strings.Repeat("█", int(math.Round(pct*20))))
		}
	}
}

func clearWatchScreen() {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Fprint(os.Stdout, "\033[H\033[2J")
}

func requestWeight(req requestRecord, pricingCfg pricingConfig) float64 {
	rates := pricingForModel(req.Model, pricingCfg)
	switch strings.ToLower(*unitFlag) {
	case "requests":
		return 1
	case "tokens":
		return float64(req.InputTokens + req.OutputTokens + req.CacheRead + req.CacheCreate)
	default:
		return float64(req.InputTokens)*rates.Input + float64(req.OutputTokens)*rates.Output + float64(req.CacheRead)*rates.CacheRead + float64(req.CacheCreate)*rates.CacheCreate
	}
}

func effectiveParallelWindow() time.Duration {
	window := *parallelWindowFlag
	if *sinceFlag == "" {
		return window
	}
	since, err := ccpaths.ParseDuration(*sinceFlag)
	if err != nil || since <= 0 {
		return window
	}
	if since < 2*time.Hour && window < 5*time.Minute {
		return 5 * time.Minute
	}
	return window
}

type pricing struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheRead   float64 `json:"cache_read"`
	CacheCreate float64 `json:"cache_create"`
}

var defaultPricing = pricing{Input: 1.0, Output: 1.0, CacheRead: 0.10, CacheCreate: 0.20}

func loadPricingConfig(path string) (pricingConfig, error) {
	cfg := pricingConfig{Models: make(map[string]pricing)}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pricingConfig{}, fmt.Errorf("read pricing file: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return pricingConfig{}, fmt.Errorf("parse pricing file: %w", err)
	}
	if cfg.Models == nil {
		cfg.Models = make(map[string]pricing)
	}
	return cfg, nil
}

func pricingForModel(model string, cfg pricingConfig) pricing {
	if p, ok := cfg.Models[model]; ok {
		return p
	}
	if p, ok := cfg.Models[modelFamily(model)]; ok {
		return p
	}
	if cfg.Default != nil {
		return *cfg.Default
	}
	switch modelFamily(model) {
	case "opus":
		return pricing{Input: 1.5, Output: 1.5, CacheRead: 0.15, CacheCreate: 0.30}
	case "haiku":
		return pricing{Input: 0.3, Output: 0.3, CacheRead: 0.03, CacheCreate: 0.06}
	default:
		return defaultPricing
	}
}

func modelFamily(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "opus"):
		return "opus"
	case strings.Contains(lower, "haiku"):
		return "haiku"
	case strings.Contains(lower, "sonnet"):
		return "sonnet"
	default:
		if lower == "" {
			return "unknown"
		}
		return lower
	}
}

func renderUnitLabel(unit string) string {
	switch strings.ToLower(unit) {
	case "requests":
		return "requests"
	case "tokens":
		return "tokens"
	default:
		return "cost-weighted tokens"
	}
}

func pct(weight, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return weight / total
}
