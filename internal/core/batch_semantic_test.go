package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeOperationPath(t *testing.T) {
	t.Parallel()

	got := NormalizeOperationPath(" https://provider.example/v1/responses/?foo=bar ")
	require.Equal(t, "/v1/responses", got)
}

func TestBatchItemRequestedModelSelector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		defaultEndpoint string
		item            BatchRequestItem
		want            string
	}{
		{
			name:            "chat default endpoint",
			defaultEndpoint: "/v1/chat/completions",
			item: BatchRequestItem{
				Body: json.RawMessage(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
			},
			want: "gpt-4o-mini",
		},
		{
			name:            "responses full url",
			defaultEndpoint: "/v1/chat/completions",
			item: BatchRequestItem{
				URL:  "https://provider.example/v1/responses/",
				Body: json.RawMessage(`{"model":"gpt-4o-mini","provider":"openai","input":"hi"}`),
			},
			want: "openai/gpt-4o-mini",
		},
		{
			name:            "embeddings explicit method",
			defaultEndpoint: "/v1/chat/completions",
			item: BatchRequestItem{
				Method: "POST",
				URL:    "/v1/embeddings",
				Body:   json.RawMessage(`{"model":"text-embedding-3-small","input":"hi"}`),
			},
			want: "text-embedding-3-small",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			requested, err := BatchItemRequestedModelSelector(tt.defaultEndpoint, tt.item)
			require.NoError(t, err)

			selector, err := requested.Normalize()
			require.NoError(t, err)
			got := selector.QualifiedModel()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBatchItemRequestedModelSelectorRejectsUnsupportedEndpoint(t *testing.T) {
	t.Parallel()

	_, err := BatchItemRequestedModelSelector("/v1/files", BatchRequestItem{
		URL:  "/v1/files",
		Body: json.RawMessage(`{"purpose":"batch"}`),
	})
	require.Error(t, err)
}

func TestDecodeKnownBatchItemRequest_NormalizesFullURLAndDecodesCanonicalRequest(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeKnownBatchItemRequest("/v1/chat/completions", BatchRequestItem{
		URL:  "https://provider.example/v1/responses/?foo=bar",
		Body: json.RawMessage(`{"model":"gpt-4o-mini","provider":"openai","input":"hi"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "/v1/responses", decoded.Endpoint)
	require.Equal(t, OperationResponses, decoded.Operation)

	req, ok := decoded.Request.(*ResponsesRequest)
	require.True(t, ok)
	require.NotNil(t, req, "Request = %T, want *ResponsesRequest", decoded.Request)
	require.Equal(t, "gpt-4o-mini", req.Model)
}

func TestMaybeDecodeKnownBatchItemRequest_SkipsUnmatchedOperation(t *testing.T) {
	t.Parallel()

	decoded, handled, err := MaybeDecodeKnownBatchItemRequest("/v1/chat/completions", BatchRequestItem{
		Method: "POST",
		URL:    "/v1/embeddings",
		Body:   json.RawMessage(`{"model":"text-embedding-3-small","input":"hi"}`),
	}, OperationChatCompletions, OperationResponses)
	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, decoded)
}

func TestDispatchDecodedBatchItem_RoutesTypedRequest(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeKnownBatchItemRequest("/v1/chat/completions", BatchRequestItem{
		URL:  "/v1/responses",
		Body: json.RawMessage(`{"model":"gpt-4o-mini","input":"hi"}`),
	})
	require.NoError(t, err)

	got, err := DispatchDecodedBatchItem(decoded, DecodedBatchItemHandlers[string]{
		Responses: func(req *ResponsesRequest) (string, error) {
			return req.Model, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", got)
}
