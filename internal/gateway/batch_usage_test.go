package gateway

import (
	"testing"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

type batchUsageCaptureLogger struct {
	config  usage.Config
	entries []*usage.UsageEntry
}

func (l *batchUsageCaptureLogger) Write(entry *usage.UsageEntry) {
	l.entries = append(l.entries, entry)
}

func (l *batchUsageCaptureLogger) Config() usage.Config { return l.config }
func (l *batchUsageCaptureLogger) Close() error         { return nil }

type staticBatchPricingResolver struct {
	pricing *core.ModelPricing
}

func (r staticBatchPricingResolver) ResolvePricing(_, _ string) *core.ModelPricing {
	return r.pricing
}

func TestLogBatchUsageFromBatchResultsOnlySetsObservedCostComponents(t *testing.T) {
	inputRate := 1.25
	logger := &batchUsageCaptureLogger{config: usage.Config{Enabled: true}}
	stored := &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:       "batch_cost_components",
			Provider: "openai",
		},
		RequestID: "req-batch-cost-components",
	}
	result := &core.BatchResultsResponse{
		Object:  "list",
		BatchID: "batch_cost_components",
		Data: []core.BatchResultItem{
			{
				Index:      0,
				StatusCode: 200,
				Model:      "gpt-cost-input-only",
				Provider:   "openai",
				Response: map[string]any{
					"id":    "resp-cost-input-only",
					"model": "gpt-cost-input-only",
					"usage": map[string]any{
						"input_tokens":  float64(1_000_000),
						"output_tokens": float64(10),
						"total_tokens":  float64(1_000_010),
					},
				},
			},
		},
	}

	logged := LogBatchUsageFromBatchResults(
		stored,
		result,
		"",
		logger,
		staticBatchPricingResolver{pricing: &core.ModelPricing{InputPerMtok: &inputRate}},
	)
	if !logged {
		t.Fatal("LogBatchUsageFromBatchResults() = false, want true")
	}
	if len(logger.entries) != 1 {
		t.Fatalf("logged entries = %d, want 1", len(logger.entries))
	}

	got := stored.Batch.Usage
	if got.InputCost == nil || *got.InputCost != inputRate {
		t.Fatalf("InputCost = %#v, want %.2f", got.InputCost, inputRate)
	}
	if got.OutputCost != nil {
		t.Fatalf("OutputCost = %#v, want nil for unobserved output cost", got.OutputCost)
	}
	if got.TotalCost == nil || *got.TotalCost != inputRate {
		t.Fatalf("TotalCost = %#v, want %.2f", got.TotalCost, inputRate)
	}
}
