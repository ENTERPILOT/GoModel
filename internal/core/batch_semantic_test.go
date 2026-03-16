package core

import (
	"encoding/json"
	"testing"
)

func TestNormalizeOperationPath(t *testing.T) {
	t.Parallel()

	got := NormalizeOperationPath(" https://provider.example/v1/responses/?foo=bar ")
	if got != "/v1/responses" {
		t.Fatalf("NormalizeOperationPath() = %q, want /v1/responses", got)
	}
}

func TestBatchItemModelSelector(t *testing.T) {
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			selector, err := BatchItemModelSelector(tt.defaultEndpoint, tt.item)
			if err != nil {
				t.Fatalf("BatchItemModelSelector() error = %v", err)
			}
			if got := selector.QualifiedModel(); got != tt.want {
				t.Fatalf("BatchItemModelSelector() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBatchItemModelSelectorRejectsUnsupportedEndpoint(t *testing.T) {
	t.Parallel()

	_, err := BatchItemModelSelector("/v1/files", BatchRequestItem{
		URL:  "/v1/files",
		Body: json.RawMessage(`{"purpose":"batch"}`),
	})
	if err == nil {
		t.Fatal("BatchItemModelSelector() error = nil, want unsupported endpoint error")
	}
}

func TestDecodeKnownBatchItemRequest_NormalizesFullURLAndDecodesCanonicalRequest(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeKnownBatchItemRequest("/v1/chat/completions", BatchRequestItem{
		URL:  "https://provider.example/v1/responses/?foo=bar",
		Body: json.RawMessage(`{"model":"gpt-4o-mini","provider":"openai","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("DecodeKnownBatchItemRequest() error = %v", err)
	}
	if decoded.Endpoint != "/v1/responses" {
		t.Fatalf("Endpoint = %q, want /v1/responses", decoded.Endpoint)
	}
	if decoded.Operation != OperationResponses {
		t.Fatalf("Operation = %q, want responses", decoded.Operation)
	}
	if decoded.ResponsesRequest() == nil {
		t.Fatal("ResponsesRequest = nil")
	}
	if decoded.ResponsesRequest().Model != "gpt-4o-mini" {
		t.Fatalf("ResponsesRequest.Model = %q, want gpt-4o-mini", decoded.ResponsesRequest().Model)
	}
}

func TestMaybeDecodeKnownBatchItemRequest_SkipsUnmatchedOperation(t *testing.T) {
	t.Parallel()

	decoded, handled, err := MaybeDecodeKnownBatchItemRequest("/v1/chat/completions", BatchRequestItem{
		Method: "POST",
		URL:    "/v1/embeddings",
		Body:   json.RawMessage(`{"model":"text-embedding-3-small","input":"hi"}`),
	}, OperationChatCompletions, OperationResponses)
	if err != nil {
		t.Fatalf("MaybeDecodeKnownBatchItemRequest() error = %v, want nil", err)
	}
	if handled {
		t.Fatal("MaybeDecodeKnownBatchItemRequest() handled = true, want false")
	}
	if decoded != nil {
		t.Fatalf("MaybeDecodeKnownBatchItemRequest() decoded = %#v, want nil", decoded)
	}
}

func TestDispatchDecodedBatchItem_RoutesTypedRequest(t *testing.T) {
	t.Parallel()

	decoded, err := DecodeKnownBatchItemRequest("/v1/chat/completions", BatchRequestItem{
		URL:  "/v1/responses",
		Body: json.RawMessage(`{"model":"gpt-4o-mini","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("DecodeKnownBatchItemRequest() error = %v", err)
	}

	got, err := DispatchDecodedBatchItem(decoded, DecodedBatchItemHandlers[string]{
		Responses: func(req *ResponsesRequest) (string, error) {
			return req.Model, nil
		},
	})
	if err != nil {
		t.Fatalf("DispatchDecodedBatchItem() error = %v", err)
	}
	if got != "gpt-4o-mini" {
		t.Fatalf("DispatchDecodedBatchItem() = %q, want gpt-4o-mini", got)
	}
}
