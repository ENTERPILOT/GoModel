package gateway

import (
	"math"
	"testing"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
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
	require.True(t, logged)
	require.Len(t, logger.entries, 1)

	got := stored.Batch.Usage
	require.NotNil(t, got.InputCost)
	require.Equal(t, inputRate, *got.InputCost)
	require.Nil(t, got.OutputCost)
	require.NotNil(t, got.TotalCost)
	require.Equal(t, inputRate, *got.TotalCost)
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
	require.True(t, logged)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, usage.CostSourceXAITicks, entry.CostSource)
	require.NotNil(t, entry.TotalCost)
	require.LessOrEqual(t, math.Abs(*entry.TotalCost-0.00001585), 1e-12)
	require.Nil(t, entry.InputCost)
	require.Nil(t, entry.OutputCost)
	require.NotNil(t, stored.Batch.Usage.TotalCost)
	require.LessOrEqual(t, math.Abs(*stored.Batch.Usage.TotalCost-0.00001585), 1e-12)
}
