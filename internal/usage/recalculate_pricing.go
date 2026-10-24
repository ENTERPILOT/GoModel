package usage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// RecalculatePricingParams identifies the stored usage rows whose costs should
// be recalculated from the latest model pricing metadata. Row selection
// (date range, model, provider, label, user path) rides on the embedded
// UsageQueryParams, sharing the readers' filter semantics.
type RecalculatePricingParams struct {
	UsageQueryParams

	Billing Billing
}

// Billing holds the pricing switches recalculation applies to billed cost.
type Billing struct {
	// Enabled mirrors pricing.markups_enabled. While false, recalculation
	// leaves each row's billed cost as it was.
	Enabled bool
	// BillCacheHits mirrors pricing.bill_cache_hits: whether recalculated
	// response-cache hit rows are billed or billed at zero.
	BillCacheHits bool
}

// RecalculatePricingResult summarizes a pricing recalculation run.
type RecalculatePricingResult struct {
	Status         string `json:"status"`
	Matched        int64  `json:"matched"`
	Recalculated   int64  `json:"recalculated"`
	WithPricing    int64  `json:"with_pricing"`
	WithoutPricing int64  `json:"without_pricing"`
}

// PricingRecalculator updates persisted usage cost fields from current pricing metadata.
type PricingRecalculator interface {
	RecalculatePricing(ctx context.Context, params RecalculatePricingParams, resolver PricingResolver) (RecalculatePricingResult, error)
}

type recalculationEntry struct {
	ID string
	// Timestamp selects the time-of-day pricing window the row fell in; a zero
	// value prices the row at the base (standard) rates.
	Timestamp          time.Time
	Model              string
	Provider           string
	ProviderName       string
	Endpoint           string
	InputTokens        int
	OutputTokens       int
	RewriteTokensSaved int
	RawData            map[string]any
	Caveat             string

	// CacheType and MarkupPercent are the stored row values; the billed cost
	// is recomputed from the new total cost with the markup the row was
	// billed at, so changed markup rules never rewrite past bills.
	CacheType     string
	MarkupPercent *float64
	Billing       Billing
}

type recalculationUpdate struct {
	ID               string
	InputCost        *float64
	OutputCost       *float64
	TotalCost        *float64
	RewriteCostSaved *float64
	BilledCost       *float64
	// KeepBilledCost leaves the stored billed cost unchanged (billing is
	// off), so BilledCost is not written.
	KeepBilledCost bool
	CostSource     string
	Caveat         string
	HasPricing     bool
}

func normalizedRecalculatePricingParams(params RecalculatePricingParams) RecalculatePricingParams {
	params.Model = strings.TrimSpace(params.Model)
	params.Provider = strings.TrimSpace(params.Provider)
	params.Label = strings.TrimSpace(params.Label)
	params.CacheMode = CacheModeAll
	return params
}

func recalculateEntryCosts(entry recalculationEntry, resolver PricingResolver) recalculationUpdate {
	pricingProvider := effectiveRecalculationPricingProvider(entry.Provider, entry.ProviderName)
	var pricing *core.ModelPricing
	if resolver != nil {
		pricing = resolver.ResolvePricing(entry.Model, pricingProvider)
	}
	effectivePricing := pricingForEndpoint(pricing.AtTime(entry.Timestamp), entry.Endpoint)
	result := CalculateUsageCost(entry.InputTokens, entry.OutputTokens, entry.RawData, entry.Provider, effectivePricing)
	caveat := result.Caveat
	if retained := retainedMissingUsageCaveat(entry.Caveat, entry.OutputTokens, entry.RawData, effectivePricing); retained != "" {
		if caveat == "" {
			caveat = retained
		} else {
			caveat = retained + "; " + caveat
		}
	}
	return recalculationUpdate{
		ID:         entry.ID,
		InputCost:  result.InputCost,
		OutputCost: result.OutputCost,
		TotalCost:  result.TotalCost,
		RewriteCostSaved: rewriteCostSaved(
			entry.InputTokens,
			entry.OutputTokens,
			entry.RawData,
			entry.Provider,
			result.InputCost,
			effectivePricing,
			entry.RewriteTokensSaved,
		),
		BilledCost:     entryBilledCost(entry, result.TotalCost),
		KeepBilledCost: !entry.Billing.Enabled,
		CostSource:     result.Source,
		Caveat:         caveat,
		HasPricing:     result.TotalCost != nil || result.InputCost != nil || result.OutputCost != nil,
	}
}

func effectiveRecalculationPricingProvider(provider, providerName string) string {
	if name := strings.TrimSpace(providerName); name != "" {
		return name
	}
	return strings.TrimSpace(provider)
}

func updateRecalculatePricingResult(result *RecalculatePricingResult, update recalculationUpdate) {
	result.Matched++
	result.Recalculated++
	if update.HasPricing {
		result.WithPricing++
	} else {
		result.WithoutPricing++
	}
}

func finalizeRecalculatePricingResult(result RecalculatePricingResult) RecalculatePricingResult {
	if result.Status == "" {
		result.Status = "ok"
	}
	return result
}

func rawDataFromJSON(raw, entryID string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		slog.Warn("failed to unmarshal usage raw_data for pricing recalculation", "id", entryID, "error", err)
		return nil
	}
	return data
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func recalculatePricingUnavailable(resolver PricingResolver) error {
	if resolver == nil {
		return fmt.Errorf("pricing resolver is required")
	}
	return nil
}

// entryBilledCost recomputes a row's billed cost from its new total cost and
// the markup it was billed at, or returns nil while billing is off.
func entryBilledCost(entry recalculationEntry, totalCost *float64) *float64 {
	if !entry.Billing.Enabled {
		return nil
	}
	return BilledCost(totalCost, entry.MarkupPercent, entry.CacheType != "", entry.Billing.BillCacheHits)
}
