package batch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gomodel/internal/core"
)

func TestSerializeBatchValidatesID(t *testing.T) {
	t.Run("nil batch", func(t *testing.T) {
		_, err := serializeBatch(nil)
		if err == nil {
			t.Fatal("expected error for nil batch")
		}
	})

	t.Run("empty batch id", func(t *testing.T) {
		_, err := serializeBatch(&StoredBatch{Batch: &core.BatchResponse{}})
		if err == nil {
			t.Fatal("expected error for empty batch ID")
		}
		if !strings.Contains(err.Error(), "batch ID is empty") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSerializeBatchPreservesRequestEndpointHints(t *testing.T) {
	raw, err := serializeBatch(&StoredBatch{
		Batch: &core.BatchResponse{
			ID: "batch_123",
		},
		RequestEndpointByCustomID: map[string]string{
			"resp-1": "/v1/responses",
			"chat-1": "/v1/chat/completions",
		},
	})
	if err != nil {
		t.Fatalf("serializeBatch() error = %v", err)
	}

	decoded, err := deserializeBatch(raw)
	if err != nil {
		t.Fatalf("deserializeBatch() error = %v", err)
	}
	if decoded.Batch == nil {
		t.Fatal("decoded.Batch = nil")
	}
	if got := decoded.RequestEndpointByCustomID["resp-1"]; got != "/v1/responses" {
		t.Fatalf("RequestEndpointByCustomID[resp-1] = %q, want /v1/responses", got)
	}
	if got := decoded.RequestEndpointByCustomID["chat-1"]; got != "/v1/chat/completions" {
		t.Fatalf("RequestEndpointByCustomID[chat-1] = %q, want /v1/chat/completions", got)
	}
}

func TestDeserializeBatchSupportsLegacyPayloads(t *testing.T) {
	raw, err := json.Marshal(&core.BatchResponse{
		ID:        "batch_legacy",
		Object:    "batch",
		Status:    "completed",
		CreatedAt: 123,
	})
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}

	decoded, err := deserializeBatch(raw)
	if err != nil {
		t.Fatalf("deserializeBatch() error = %v", err)
	}
	if decoded.Batch == nil {
		t.Fatal("decoded.Batch = nil")
	}
	if decoded.Batch.ID != "batch_legacy" {
		t.Fatalf("decoded.Batch.ID = %q, want batch_legacy", decoded.Batch.ID)
	}
	if len(decoded.RequestEndpointByCustomID) != 0 {
		t.Fatalf("RequestEndpointByCustomID = %#v, want empty", decoded.RequestEndpointByCustomID)
	}
}

func TestNewRequiresConfig(t *testing.T) {
	_, err := New(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if !strings.Contains(err.Error(), "config is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
