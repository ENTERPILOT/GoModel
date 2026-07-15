package pricingoverrides

import (
	"time"

	"github.com/enterpilot/gomodel/internal/modelselectors"
)

const CurrencyUSD = "USD"

// Pricing stores operator-supplied pricing fields. Currency is intentionally
// omitted from persistence and API payloads; UI-managed pricing is USD-only.
type Pricing struct {
	InputPerMtok           *float64      `json:"input_per_mtok,omitempty" bson:"input_per_mtok,omitempty"`
	OutputPerMtok          *float64      `json:"output_per_mtok,omitempty" bson:"output_per_mtok,omitempty"`
	CachedInputPerMtok     *float64      `json:"cached_input_per_mtok,omitempty" bson:"cached_input_per_mtok,omitempty"`
	CacheWritePerMtok      *float64      `json:"cache_write_per_mtok,omitempty" bson:"cache_write_per_mtok,omitempty"`
	ReasoningOutputPerMtok *float64      `json:"reasoning_output_per_mtok,omitempty" bson:"reasoning_output_per_mtok,omitempty"`
	BatchInputPerMtok      *float64      `json:"batch_input_per_mtok,omitempty" bson:"batch_input_per_mtok,omitempty"`
	BatchOutputPerMtok     *float64      `json:"batch_output_per_mtok,omitempty" bson:"batch_output_per_mtok,omitempty"`
	AudioInputPerMtok      *float64      `json:"audio_input_per_mtok,omitempty" bson:"audio_input_per_mtok,omitempty"`
	AudioOutputPerMtok     *float64      `json:"audio_output_per_mtok,omitempty" bson:"audio_output_per_mtok,omitempty"`
	PerImage               *float64      `json:"per_image,omitempty" bson:"per_image,omitempty"`
	InputPerImage          *float64      `json:"input_per_image,omitempty" bson:"input_per_image,omitempty"`
	PerSecondInput         *float64      `json:"per_second_input,omitempty" bson:"per_second_input,omitempty"`
	PerSecondOutput        *float64      `json:"per_second_output,omitempty" bson:"per_second_output,omitempty"`
	PerCharacterInput      *float64      `json:"per_character_input,omitempty" bson:"per_character_input,omitempty"`
	PerRequest             *float64      `json:"per_request,omitempty" bson:"per_request,omitempty"`
	PerPage                *float64      `json:"per_page,omitempty" bson:"per_page,omitempty"`
	Tiers                  []PricingTier `json:"tiers,omitempty" bson:"tiers,omitempty"`
}

// PricingTier stores future tiered pricing without changing the DB schema.
type PricingTier struct {
	UpToTokens    *float64 `json:"up_to_tokens,omitempty" bson:"up_to_tokens,omitempty"`
	UpToMtok      *float64 `json:"up_to_mtok,omitempty" bson:"up_to_mtok,omitempty"`
	InputPerMtok  *float64 `json:"input_per_mtok,omitempty" bson:"input_per_mtok,omitempty"`
	OutputPerMtok *float64 `json:"output_per_mtok,omitempty" bson:"output_per_mtok,omitempty"`
}

// Override stores one persisted pricing override for a model selector.
type Override struct {
	Selector     string    `json:"selector" bson:"_id"`
	ProviderName string    `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Model        string    `json:"model,omitempty" bson:"model,omitempty"`
	Pricing      Pricing   `json:"pricing" bson:"pricing"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// ScopeKind identifies how broadly an override applies.
type ScopeKind = modelselectors.ScopeKind

// ScopeKind reports the normalized selector scope for one override.
func (o Override) ScopeKind() ScopeKind {
	return modelselectors.ScopeKindFor(o.Selector, o.ProviderName, o.Model)
}

// View is the admin-facing representation of one persisted pricing override.
type View struct {
	Override
	ScopeKind ScopeKind `json:"scope_kind"`
}

// Catalog is the minimal configured-provider surface needed for selector validation.
type Catalog = modelselectors.Catalog

func normalizeOverrideInput(catalog Catalog, override Override) (Override, error) {
	parts, err := modelselectors.NormalizeInput(catalog, override.Selector)
	if err != nil {
		return Override{}, err
	}
	override.Selector = parts.Selector
	override.ProviderName = parts.ProviderName
	override.Model = parts.Model
	return normalizeOverridePricing(override)
}

func normalizeStoredOverride(override Override) (Override, error) {
	parts, err := modelselectors.NormalizeStored(override.Selector, override.ProviderName, override.Model)
	if err != nil {
		return Override{}, err
	}
	override.Selector = parts.Selector
	override.ProviderName = parts.ProviderName
	override.Model = parts.Model
	return normalizeOverridePricing(override)
}

func normalizeOverridePricing(override Override) (Override, error) {
	pricing := clonePricing(override.Pricing)
	if pricingEmpty(pricing) {
		return Override{}, newValidationError("pricing is required", nil)
	}
	if err := validatePricing(pricing); err != nil {
		return Override{}, err
	}
	override.Pricing = pricing
	return override, nil
}
