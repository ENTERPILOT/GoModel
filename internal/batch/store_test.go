package batch

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestSerializeBatchValidatesID(t *testing.T) {
	t.Run("nil batch", func(t *testing.T) {
		_, err := serializeBatch(nil)
		require.Error(t, err)

	})

	t.Run("empty batch id", func(t *testing.T) {
		_, err := serializeBatch(&StoredBatch{Batch: &core.BatchResponse{}})
		require.Error(t, err)
		require.Contains(t, err.Error(), "batch ID is empty")

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
		OriginalInputFileID:  "file_original",
		RewrittenInputFileID: "file_rewritten",
	})
	require.NoError(t, err)

	decoded, err := deserializeBatch(raw)
	require.NoError(t, err)
	require.NotNil(t, decoded.Batch)
	got := decoded.RequestEndpointByCustomID["resp-1"]
	require.Equal(t, "/v1/responses", got)
	got = decoded.RequestEndpointByCustomID["chat-1"]
	require.Equal(t, "/v1/chat/completions", got)
	require.Equal(t, "file_original", decoded.OriginalInputFileID)
	require.Equal(t, "file_rewritten", decoded.RewrittenInputFileID)
}

func TestSerializeBatchPreservesUserPath(t *testing.T) {
	raw, err := serializeBatch(&StoredBatch{
		Batch: &core.BatchResponse{
			ID: "batch_123",
		},
		UserPath: "/team/alpha",
	})
	require.NoError(t, err)

	decoded, err := deserializeBatch(raw)
	require.NoError(t, err)
	got := decoded.UserPath
	require.Equal(t, "/team/alpha", got)
}

func TestSerializeBatchStripsGatewayOnlyMetadata(t *testing.T) {
	loggedAt := time.Unix(1700000000, 0).UTC()
	raw, err := serializeBatch(&StoredBatch{
		Batch: &core.BatchResponse{
			ID: "batch_123",
			Metadata: map[string]string{
				"visible":                "keep",
				RequestIDMetadataKey:     "req_123",
				UsageLoggedAtMetadataKey: strconv.FormatInt(loggedAt.Unix(), 10),
			},
		},
	})
	require.NoError(t, err)

	decoded, err := deserializeBatch(raw)
	require.NoError(t, err)
	require.NotNil(t, decoded.Batch)
	require.Empty(t, decoded.Batch.Metadata[RequestIDMetadataKey])
	require.Empty(t, decoded.Batch.Metadata[UsageLoggedAtMetadataKey])
	require.Equal(t, "keep", decoded.Batch.Metadata["visible"])
	require.Equal(t, "req_123", decoded.RequestID)
	require.NotNil(t, decoded.UsageLoggedAt)
	require.True(t, decoded.UsageLoggedAt.Equal(loggedAt), "UsageLoggedAt = %v, want %v", decoded.UsageLoggedAt, loggedAt)
}

func TestDeserializeBatchSupportsLegacyPayloads(t *testing.T) {
	raw, err := json.Marshal(&core.BatchResponse{
		ID:        "batch_legacy",
		Object:    "batch",
		Status:    "completed",
		CreatedAt: 123,
	})
	require.NoError(t, err)

	decoded, err := deserializeBatch(raw)
	require.NoError(t, err)
	require.NotNil(t, decoded.Batch)
	require.Equal(t, "batch_legacy", decoded.Batch.ID)
	require.Empty(t, decoded.RequestEndpointByCustomID)
}

func TestDeserializeBatchRejectsLegacyPayloadWithoutID(t *testing.T) {
	raw, err := json.Marshal(&core.BatchResponse{Object: "batch"})
	require.NoError(t, err)

	_, err = deserializeBatch(raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy batch missing ID")
}

func TestNewRequiresSharedStorage(t *testing.T) {
	_, err := New(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shared storage is required")
}
