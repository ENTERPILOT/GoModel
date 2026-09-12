package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestHandlerLogBatchUsageFromBatchResultsUsesStoredUserPath(t *testing.T) {
	logger := &usageCaptureLogger{
		config: usage.Config{Enabled: true},
	}
	handler := &Handler{
		usageLogger: logger,
	}

	stored := &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:       "batch_123",
			Provider: "openai",
		},
		RequestID: "req-batch",
		UserPath:  "/team/alpha",
	}
	result := &core.BatchResultsResponse{
		Object:  "list",
		BatchID: "batch_123",
		Data: []core.BatchResultItem{
			{
				Index:      0,
				StatusCode: 200,
				Model:      "gpt-5",
				Provider:   "openai",
				Response: map[string]any{
					"id":    "resp-1",
					"model": "gpt-5",
					"usage": map[string]any{
						"input_tokens":  float64(10),
						"output_tokens": float64(5),
						"total_tokens":  float64(15),
					},
				},
			},
		},
	}

	logged := gateway.LogBatchUsageFromBatchResults(stored, result, "", handler.usageLogger, handler.pricingResolver)
	require.True(t, logged)

	entries := logger.Entries()
	require.Len(t, entries, 1)
	require.Equal(t, "/team/alpha", entries[0].UserPath)
}
