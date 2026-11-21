package usage

import (
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	endpointModerations = "/v1/moderations"

	// rawKeyModerationInputs carries the number of inputs a moderation call
	// classified, so the call stays observable without token usage.
	rawKeyModerationInputs = "moderation_inputs"
)

// ExtractFromModerationResponse builds a usage entry for a moderation call.
// Neither OpenAI nor Mistral reports token usage here, so the entry records
// the call and its input count. OpenAI moderation is free; a model priced by
// token (Mistral) is flagged as uncosted rather than shown as $0. model is the
// resolved route model so the row groups and prices with the pricing lookup.
func ExtractFromModerationResponse(resp *core.ModerationResponse, requestID, model, provider string, pricing ...*core.ModelPricing) *UsageEntry {
	if resp == nil {
		return nil
	}
	entry := &UsageEntry{
		ID:         uuid.New().String(),
		RequestID:  requestID,
		ProviderID: resp.ID,
		Timestamp:  time.Now().UTC(),
		Model:      model,
		Provider:   provider,
		Endpoint:   endpointModerations,
		RawData:    map[string]any{rawKeyModerationInputs: len(resp.Results)},
	}
	applyUsageCosts(entry, provider, endpointModerations, pricing...)
	if !isProviderReportedCostSource(entry.CostSource) &&
		tokenRatesAffectCost(effectiveEndpointPricing(endpointModerations, entry.Timestamp, pricing...)) &&
		entry.CostsCalculationCaveat == "" {
		entry.CostsCalculationCaveat = caveatEmbeddingMissingUsage
	}
	return entry
}
