package usage

import "github.com/enterpilot/gomodel/internal/core"

// PricingResolver resolves pricing metadata for a given model and provider type.
// Implementations should check the registry first and fall back to a reverse-index
// lookup when the model ID in the usage DB differs from the registry key.
type PricingResolver interface {
	ResolvePricing(model, providerType string) *core.ModelPricing
}
