package ccusage

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Pricing weights the four token counts of a request against each other. The
// numbers are relative, not currency: a cache read costing 0.10 means it is
// weighted at a tenth of an uncached input token.
type Pricing struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheRead   float64 `json:"cache_read"`
	CacheCreate float64 `json:"cache_create"`
}

// PricingConfig overrides the built-in rates. Models is keyed by either an
// exact model string or a family from [ModelFamily]; Default applies to models
// no key matches.
type PricingConfig struct {
	Default *Pricing           `json:"default,omitempty"`
	Models  map[string]Pricing `json:"models,omitempty"`
}

// DefaultPricing is the rate set for a model with no more specific entry.
var DefaultPricing = Pricing{Input: 1.0, Output: 1.0, CacheRead: 0.10, CacheCreate: 0.20}

// LoadPricing reads a pricing config from a JSON file. An empty path yields an
// empty config, not an error, so callers can pass an unset flag straight in.
func LoadPricing(path string) (PricingConfig, error) {
	cfg := PricingConfig{Models: make(map[string]Pricing)}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PricingConfig{}, fmt.Errorf("read pricing file: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return PricingConfig{}, fmt.Errorf("parse pricing file: %w", err)
	}
	if cfg.Models == nil {
		cfg.Models = make(map[string]Pricing)
	}
	return cfg, nil
}

// PricingForModel returns the rates for model: an exact entry in cfg, else a
// family entry, else cfg's default, else the built-in family rates.
func PricingForModel(model string, cfg PricingConfig) Pricing {
	if p, ok := cfg.Models[model]; ok {
		return p
	}
	if p, ok := cfg.Models[ModelFamily(model)]; ok {
		return p
	}
	if cfg.Default != nil {
		return *cfg.Default
	}
	switch ModelFamily(model) {
	case "opus":
		return Pricing{Input: 1.5, Output: 1.5, CacheRead: 0.15, CacheCreate: 0.30}
	case "haiku":
		return Pricing{Input: 0.3, Output: 0.3, CacheRead: 0.03, CacheCreate: 0.06}
	default:
		return DefaultPricing
	}
}

// ModelFamily returns the pricing family of a model string: "opus", "haiku",
// "sonnet", "unknown" for the empty string, or the lowercased model itself.
// Returning the model unchanged is what lets a caller notice a model it has no
// family for.
func ModelFamily(model string) string {
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

// Weight returns r's size in unit, for ranking requests against each other:
//
//	requests          1, so a sum counts requests
//	input             uncached prompt tokens
//	output            completion tokens
//	context           prompt tokens the model read
//	total, tokens     context plus output
//	cost              the four counts weighted by p
//
// An unrecognized unit is weighted as cost, matching what ccstats has always
// done with its -unit flag.
func Weight(r Record, unit string, p Pricing) float64 {
	switch strings.ToLower(unit) {
	case "requests":
		return 1
	case "input":
		return float64(r.Input)
	case "output":
		return float64(r.Output)
	case "context":
		return float64(r.Context())
	case "total", "tokens":
		return float64(r.Total())
	default:
		return float64(r.Input)*p.Input + float64(r.Output)*p.Output +
			float64(r.CacheRead)*p.CacheRead + float64(r.CacheCreate)*p.CacheCreate
	}
}
