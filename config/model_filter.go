package config

import "strings"

// ModelFilter narrows a provider's discovered model inventory. It is declared
// under providers.<name>.model_filter and applied after metadata enrichment, so
// price rules see both registry pricing and whatever the provider reported.
//
//	providers:
//	  openrouter:
//	    model_filter:
//	      include: ["*:free"]      # keep only matching models
//	      exclude: ["*-preview"]   # then drop matching models
//	      max_price_per_mtok: 0    # drop models priced above the cap
//
// Patterns are globs matched case-insensitively against the raw provider model
// ID: `*` matches any run of characters (including `/`, unlike shell globs, so
// `*:free` matches `deepseek/deepseek-r1:free`) and `?` matches exactly one.
type ModelFilter struct {
	// Include keeps only models matching at least one pattern. Empty keeps all.
	Include []string `yaml:"include"`
	// Exclude drops models matching any pattern. It is applied after Include.
	Exclude []string `yaml:"exclude"`
	// MaxPricePerMtok caps a model's highest per-million-token rate — the
	// larger of its input and output rate. A model with no known pricing is
	// dropped while the cap is set: a price cap that lets unpriced models
	// through is not a cap. Nil disables price filtering.
	MaxPricePerMtok *float64 `yaml:"max_price_per_mtok"`
}

// Normalize trims patterns and drops empty ones.
func (f ModelFilter) Normalize() ModelFilter {
	return ModelFilter{
		Include:         normalizeModelFilterPatterns(f.Include),
		Exclude:         normalizeModelFilterPatterns(f.Exclude),
		MaxPricePerMtok: f.MaxPricePerMtok,
	}
}

// Empty reports whether the filter would keep every model.
func (f ModelFilter) Empty() bool {
	return len(normalizeModelFilterPatterns(f.Include)) == 0 &&
		len(normalizeModelFilterPatterns(f.Exclude)) == 0 &&
		f.MaxPricePerMtok == nil
}

func normalizeModelFilterPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			out = append(out, pattern)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
