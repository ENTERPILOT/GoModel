package modeldata

import (
	"gomodel/internal/core"
	"maps"
)

// MergeMetadata merges override onto base field-wise. Non-zero override fields
// win; zero override fields preserve the base value. Capabilities and Rankings
// are merged key-by-key (override keys replace base keys). Returns a new
// *ModelMetadata; inputs are not mutated and the returned value never shares
// slice, map, or pointer backing storage with base or override.
//
// This supports config-driven overrides layered on top of remote-registry
// enrichment: operators can specify just the fields they care about (e.g.
// context_window for a local Ollama model) without clobbering unrelated fields.
func MergeMetadata(base, override *core.ModelMetadata) *core.ModelMetadata {
	if override == nil {
		return base.Clone()
	}
	if base == nil {
		return override.Clone()
	}

	merged := base.Clone()

	if override.DisplayName != "" {
		merged.DisplayName = override.DisplayName
	}
	if override.Description != "" {
		merged.Description = override.Description
	}
	if override.Family != "" {
		merged.Family = override.Family
	}
	if len(override.Modes) > 0 {
		merged.Modes = append([]string(nil), override.Modes...)
	}
	if len(override.Categories) > 0 {
		merged.Categories = append([]core.ModelCategory(nil), override.Categories...)
	}
	if len(override.Tags) > 0 {
		merged.Tags = append([]string(nil), override.Tags...)
	}
	if override.ContextWindow != nil {
		v := *override.ContextWindow
		merged.ContextWindow = &v
	}
	if override.MaxOutputTokens != nil {
		v := *override.MaxOutputTokens
		merged.MaxOutputTokens = &v
	}
	if len(override.Capabilities) > 0 {
		out := make(map[string]bool, len(merged.Capabilities)+len(override.Capabilities))
		maps.Copy(out, merged.Capabilities)
		maps.Copy(out, override.Capabilities)
		merged.Capabilities = out
	}
	if len(override.Rankings) > 0 {
		out := make(map[string]core.ModelRanking, len(merged.Rankings)+len(override.Rankings))
		// merged.Rankings is already a deep clone from base.Clone(), so we can
		// reuse its values directly; override values must be deep-cloned so the
		// result does not share Elo/Rank pointers with the caller's input.
		maps.Copy(out, merged.Rankings)
		for k, v := range override.Rankings {
			out[k] = core.CloneModelRanking(v)
		}
		merged.Rankings = out
	}
	if override.Pricing != nil {
		merged.Pricing = override.Pricing.Clone()
		if len(override.PricingSources) > 0 {
			merged.PricingSources = clonePricingSources(override.PricingSources)
		} else {
			merged.PricingSources = nil
		}
	}

	return merged
}

func clonePricingSources(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
