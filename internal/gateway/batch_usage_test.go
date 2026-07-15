package gateway

import (
	"math"
	"testing"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
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

func TestLogBatchUsageFromBatchResultsUsesXAITicks(t *testing.T) {
	outputRate := 999.0
	logger := &batchUsageCaptureLogger{config: usage.Config{Enabled: true}}
	stored := &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:       "batch_xai_ticks",
			Provider: "xai",
		},
		RequestID: "req-batch-xai-ticks",
	}
	result := &core.BatchResultsResponse{
		Object:  "list",
		BatchID: "batch_xai_ticks",
		Data: []core.BatchResultItem{
			{
				Index:      0,
				StatusCode: 200,
				Model:      "grok-4.3",
				Provider:   "xai",
				Response: map[string]any{
					"id":    "resp-xai-batch",
					"model": "grok-4.3",
					"usage": map[string]any{
						"input_tokens":      float64(199),
						"output_tokens":     float64(1),
						"total_tokens":      float64(200),
						"cost_in_usd_ticks": float64(158_500),
						"num_sources_used":  float64(2),
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
		staticBatchPricingResolver{pricing: &core.ModelPricing{OutputPerMtok: &outputRate}},
	)
	if !logged {
		t.Fatal("LogBatchUsageFromBatchResults() = false, want true")
	}
	if len(logger.entries) != 1 {
		t.Fatalf("logged entries = %d, want 1", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.CostSource != usage.CostSourceXAITicks {
		t.Fatalf("CostSource = %q, want %q", entry.CostSource, usage.CostSourceXAITicks)
	}
	if entry.TotalCost == nil || math.Abs(*entry.TotalCost-0.00001585) > 1e-12 {
		t.Fatalf("TotalCost = %#v, want 0.00001585", entry.TotalCost)
	}
	if entry.InputCost != nil || entry.OutputCost != nil {
		t.Fatalf("InputCost/OutputCost = %#v/%#v, want nil response split", entry.InputCost, entry.OutputCost)
	}
	if stored.Batch.Usage.TotalCost == nil || math.Abs(*stored.Batch.Usage.TotalCost-0.00001585) > 1e-12 {
		t.Fatalf("stored TotalCost = %#v, want 0.00001585", stored.Batch.Usage.TotalCost)
	}
}
