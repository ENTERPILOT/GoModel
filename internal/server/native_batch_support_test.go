package server

import (
	"testing"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/usage"
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

	logged := handler.logBatchUsageFromBatchResults(stored, result, "")
	if !logged {
		t.Fatal("logBatchUsageFromBatchResults() = false, want true")
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}
	if got := logger.entries[0].UserPath; got != "/team/alpha" {
		t.Fatalf("UserPath = %q, want /team/alpha", got)
	}
}
