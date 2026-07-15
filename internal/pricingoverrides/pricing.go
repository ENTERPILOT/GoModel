package pricingoverrides

import (
	"github.com/enterpilot/gomodel/internal/core"
)

func clonePricing(p Pricing) Pricing {
	out := p
	out.InputPerMtok = cloneFloatPtr(p.InputPerMtok)
	out.OutputPerMtok = cloneFloatPtr(p.OutputPerMtok)
	out.CachedInputPerMtok = cloneFloatPtr(p.CachedInputPerMtok)
	out.CacheWritePerMtok = cloneFloatPtr(p.CacheWritePerMtok)
	out.ReasoningOutputPerMtok = cloneFloatPtr(p.ReasoningOutputPerMtok)
	out.BatchInputPerMtok = cloneFloatPtr(p.BatchInputPerMtok)
	out.BatchOutputPerMtok = cloneFloatPtr(p.BatchOutputPerMtok)
	out.AudioInputPerMtok = cloneFloatPtr(p.AudioInputPerMtok)
	out.AudioOutputPerMtok = cloneFloatPtr(p.AudioOutputPerMtok)
	out.PerImage = cloneFloatPtr(p.PerImage)
	out.InputPerImage = cloneFloatPtr(p.InputPerImage)
	out.PerSecondInput = cloneFloatPtr(p.PerSecondInput)
	out.PerSecondOutput = cloneFloatPtr(p.PerSecondOutput)
	out.PerCharacterInput = cloneFloatPtr(p.PerCharacterInput)
	out.PerRequest = cloneFloatPtr(p.PerRequest)
	out.PerPage = cloneFloatPtr(p.PerPage)
	if len(p.Tiers) > 0 {
		out.Tiers = make([]PricingTier, len(p.Tiers))
		for i, tier := range p.Tiers {
			out.Tiers[i] = PricingTier{
				UpToTokens:    cloneFloatPtr(tier.UpToTokens),
				UpToMtok:      cloneFloatPtr(tier.UpToMtok),
				InputPerMtok:  cloneFloatPtr(tier.InputPerMtok),
				OutputPerMtok: cloneFloatPtr(tier.OutputPerMtok),
			}
		}
	} else {
		out.Tiers = nil
	}
	return out
}

func pricingEmpty(p Pricing) bool {
	if p.InputPerMtok != nil ||
		p.OutputPerMtok != nil ||
		p.CachedInputPerMtok != nil ||
		p.CacheWritePerMtok != nil ||
		p.ReasoningOutputPerMtok != nil ||
		p.BatchInputPerMtok != nil ||
		p.BatchOutputPerMtok != nil ||
		p.AudioInputPerMtok != nil ||
		p.AudioOutputPerMtok != nil ||
		p.PerImage != nil ||
		p.InputPerImage != nil ||
		p.PerSecondInput != nil ||
		p.PerSecondOutput != nil ||
		p.PerCharacterInput != nil ||
		p.PerRequest != nil ||
		p.PerPage != nil {
		return false
	}
	return len(p.Tiers) == 0
}

func overrideClone(override Override) Override {
	override.Pricing = clonePricing(override.Pricing)
	return override
}

func cloneFloatPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func pricingToCore(p Pricing) *core.ModelPricing {
	out := &core.ModelPricing{
		Currency:               CurrencyUSD,
		InputPerMtok:           cloneFloatPtr(p.InputPerMtok),
		OutputPerMtok:          cloneFloatPtr(p.OutputPerMtok),
		CachedInputPerMtok:     cloneFloatPtr(p.CachedInputPerMtok),
		CacheWritePerMtok:      cloneFloatPtr(p.CacheWritePerMtok),
		ReasoningOutputPerMtok: cloneFloatPtr(p.ReasoningOutputPerMtok),
		BatchInputPerMtok:      cloneFloatPtr(p.BatchInputPerMtok),
		BatchOutputPerMtok:     cloneFloatPtr(p.BatchOutputPerMtok),
		AudioInputPerMtok:      cloneFloatPtr(p.AudioInputPerMtok),
		AudioOutputPerMtok:     cloneFloatPtr(p.AudioOutputPerMtok),
		PerImage:               cloneFloatPtr(p.PerImage),
		InputPerImage:          cloneFloatPtr(p.InputPerImage),
		PerSecondInput:         cloneFloatPtr(p.PerSecondInput),
		PerSecondOutput:        cloneFloatPtr(p.PerSecondOutput),
		PerCharacterInput:      cloneFloatPtr(p.PerCharacterInput),
		PerRequest:             cloneFloatPtr(p.PerRequest),
		PerPage:                cloneFloatPtr(p.PerPage),
	}
	if len(p.Tiers) > 0 {
		out.Tiers = make([]core.ModelPricingTier, len(p.Tiers))
		for i, tier := range p.Tiers {
			out.Tiers[i] = core.ModelPricingTier{
				UpToTokens:    cloneFloatPtr(tier.UpToTokens),
				UpToMtok:      cloneFloatPtr(tier.UpToMtok),
				InputPerMtok:  cloneFloatPtr(tier.InputPerMtok),
				OutputPerMtok: cloneFloatPtr(tier.OutputPerMtok),
			}
		}
	}
	return out
}

func mergePricing(base *core.ModelPricing, override Pricing) *core.ModelPricing {
	var out *core.ModelPricing
	if base != nil {
		out = base.Clone()
	} else {
		out = &core.ModelPricing{}
	}
	out.Currency = CurrencyUSD

	overlay := pricingToCore(override)
	applyFloatOverride(&out.InputPerMtok, overlay.InputPerMtok)
	applyFloatOverride(&out.OutputPerMtok, overlay.OutputPerMtok)
	applyFloatOverride(&out.CachedInputPerMtok, overlay.CachedInputPerMtok)
	applyFloatOverride(&out.CacheWritePerMtok, overlay.CacheWritePerMtok)
	applyFloatOverride(&out.ReasoningOutputPerMtok, overlay.ReasoningOutputPerMtok)
	applyFloatOverride(&out.BatchInputPerMtok, overlay.BatchInputPerMtok)
	applyFloatOverride(&out.BatchOutputPerMtok, overlay.BatchOutputPerMtok)
	applyFloatOverride(&out.AudioInputPerMtok, overlay.AudioInputPerMtok)
	applyFloatOverride(&out.AudioOutputPerMtok, overlay.AudioOutputPerMtok)
	applyFloatOverride(&out.PerImage, overlay.PerImage)
	applyFloatOverride(&out.InputPerImage, overlay.InputPerImage)
	applyFloatOverride(&out.PerSecondInput, overlay.PerSecondInput)
	applyFloatOverride(&out.PerSecondOutput, overlay.PerSecondOutput)
	applyFloatOverride(&out.PerCharacterInput, overlay.PerCharacterInput)
	applyFloatOverride(&out.PerRequest, overlay.PerRequest)
	applyFloatOverride(&out.PerPage, overlay.PerPage)
	if len(overlay.Tiers) > 0 {
		out.Tiers = overlay.Tiers
	}
	return out
}

func applyFloatOverride(target **float64, value *float64) {
	if value != nil {
		*target = cloneFloatPtr(value)
	}
}
