package providers

import "github.com/enterpilot/gomodel/internal/core"

func metadataPricing(metadata *core.ModelMetadata) *core.ModelPricing {
	if metadata == nil {
		return nil
	}
	return metadata.Pricing
}

func metadataPricingSources(metadata *core.ModelMetadata) map[string]string {
	if metadata == nil {
		return nil
	}
	return metadata.PricingSources
}

func mergePricingSources(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(override))
	for key, source := range base {
		if source != "" {
			merged[key] = source
		}
	}
	for key, source := range override {
		if source != "" {
			merged[key] = source
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func mergeConfigPricing(base, override *core.ModelPricing) *core.ModelPricing {
	if override == nil {
		return base.Clone()
	}
	var merged *core.ModelPricing
	if base != nil {
		merged = base.Clone()
	} else {
		merged = &core.ModelPricing{}
	}
	if override.Currency != "" {
		merged.Currency = override.Currency
	}
	applyPricingOverride(&merged.InputPerMtok, override.InputPerMtok)
	applyPricingOverride(&merged.OutputPerMtok, override.OutputPerMtok)
	applyPricingOverride(&merged.CachedInputPerMtok, override.CachedInputPerMtok)
	applyPricingOverride(&merged.CacheWritePerMtok, override.CacheWritePerMtok)
	applyPricingOverride(&merged.ReasoningOutputPerMtok, override.ReasoningOutputPerMtok)
	applyPricingOverride(&merged.BatchInputPerMtok, override.BatchInputPerMtok)
	applyPricingOverride(&merged.BatchOutputPerMtok, override.BatchOutputPerMtok)
	applyPricingOverride(&merged.AudioInputPerMtok, override.AudioInputPerMtok)
	applyPricingOverride(&merged.AudioOutputPerMtok, override.AudioOutputPerMtok)
	applyPricingOverride(&merged.PerImage, override.PerImage)
	applyPricingOverride(&merged.InputPerImage, override.InputPerImage)
	applyPricingOverride(&merged.PerSecondInput, override.PerSecondInput)
	applyPricingOverride(&merged.PerSecondOutput, override.PerSecondOutput)
	applyPricingOverride(&merged.PerCharacterInput, override.PerCharacterInput)
	applyPricingOverride(&merged.PerRequest, override.PerRequest)
	applyPricingOverride(&merged.PerPage, override.PerPage)
	if len(override.Tiers) > 0 {
		merged.Tiers = override.Clone().Tiers
	}
	return merged
}

func applyPricingOverride(target **float64, value *float64) {
	if value == nil {
		return
	}
	copied := *value
	*target = &copied
}
