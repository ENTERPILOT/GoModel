package pricingoverrides

type pricingField struct {
	name  string
	value *float64
}

func validatePricing(p Pricing) error {
	for _, field := range pricingScalarFields(p) {
		if field.value != nil && *field.value < 0 {
			return newValidationError("pricing."+field.name+" must be greater than or equal to 0", nil)
		}
	}
	for i, tier := range p.Tiers {
		if tier.UpToTokens != nil && tier.UpToMtok != nil {
			return newValidationError("pricing.tiers must set only one threshold: up_to_tokens or up_to_mtok", nil)
		}
		if tier.UpToTokens != nil && *tier.UpToTokens <= 0 {
			return newValidationError("pricing.tiers up_to_tokens must be greater than 0", nil)
		}
		if tier.UpToMtok != nil && *tier.UpToMtok <= 0 {
			return newValidationError("pricing.tiers up_to_mtok must be greater than 0", nil)
		}
		if tier.UpToTokens == nil && tier.UpToMtok == nil {
			return newValidationError("pricing.tiers threshold is required", nil)
		}
		if tier.InputPerMtok == nil && tier.OutputPerMtok == nil {
			return newValidationError("pricing.tiers rate is required", nil)
		}
		for _, field := range pricingTierFields(tier) {
			if field.value != nil && *field.value < 0 {
				return newValidationError("pricing.tiers "+field.name+" must be greater than or equal to 0", nil)
			}
		}
		if i > 0 && tierLimit(tier) <= tierLimit(p.Tiers[i-1]) {
			return newValidationError("pricing.tiers thresholds must be increasing", nil)
		}
	}
	return nil
}

func pricingScalarFields(p Pricing) []pricingField {
	return []pricingField{
		{"input_per_mtok", p.InputPerMtok},
		{"output_per_mtok", p.OutputPerMtok},
		{"cached_input_per_mtok", p.CachedInputPerMtok},
		{"cache_write_per_mtok", p.CacheWritePerMtok},
		{"reasoning_output_per_mtok", p.ReasoningOutputPerMtok},
		{"batch_input_per_mtok", p.BatchInputPerMtok},
		{"batch_output_per_mtok", p.BatchOutputPerMtok},
		{"audio_input_per_mtok", p.AudioInputPerMtok},
		{"audio_output_per_mtok", p.AudioOutputPerMtok},
		{"per_image", p.PerImage},
		{"input_per_image", p.InputPerImage},
		{"per_second_input", p.PerSecondInput},
		{"per_second_output", p.PerSecondOutput},
		{"per_character_input", p.PerCharacterInput},
		{"per_request", p.PerRequest},
		{"per_page", p.PerPage},
	}
}

func pricingTierFields(t PricingTier) []pricingField {
	return []pricingField{
		{"input_per_mtok", t.InputPerMtok},
		{"output_per_mtok", t.OutputPerMtok},
	}
}

func tierLimit(t PricingTier) float64 {
	if t.UpToTokens != nil {
		return *t.UpToTokens
	}
	if t.UpToMtok != nil {
		return *t.UpToMtok * 1_000_000
	}
	return 0
}
