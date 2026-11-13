package core

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
)

// DecodedBatchItemRequest is the canonical decode result for known JSON batch subrequests.
type DecodedBatchItemRequest struct {
	Endpoint  string
	Method    string
	Operation Operation
	Request   any
}

// DecodedBatchItemHandlers contains operation-specific handlers for a decoded
// batch item request. Downstream consumers can use this instead of switching on
// operation names directly.
type DecodedBatchItemHandlers[T any] struct {
	Chat       func(*ChatRequest) (T, error)
	Responses  func(*ResponsesRequest) (T, error)
	Embeddings func(*EmbeddingRequest) (T, error)
	Default    func(*DecodedBatchItemRequest) (T, error)
}

// RequestedModelSelector returns the raw selector requested by the decoded batch
// item, preserving whether the provider came from the explicit field.
func (decoded *DecodedBatchItemRequest) RequestedModelSelector() (RequestedModelSelector, error) {
	if decoded == nil {
		return RequestedModelSelector{}, fmt.Errorf("decoded batch request is required")
	}
	model, provider, ok := semanticSelectorFromCanonicalRequest(decoded.Request)
	if !ok {
		return RequestedModelSelector{}, fmt.Errorf("unsupported batch item url: %s", decoded.Endpoint)
	}
	return NewRequestedModelSelector(model, provider), nil
}

// DispatchDecodedBatchItem routes a decoded batch item to the matching typed
// handler based on its canonical request payload.
func DispatchDecodedBatchItem[T any](decoded *DecodedBatchItemRequest, handlers DecodedBatchItemHandlers[T]) (T, error) {
	if decoded == nil {
		var zero T
		return zero, fmt.Errorf("decoded batch request is required")
	}

	switch req := decoded.Request.(type) {
	case *ChatRequest:
		if handlers.Chat != nil {
			return handlers.Chat(req)
		}
	case *ResponsesRequest:
		if handlers.Responses != nil {
			return handlers.Responses(req)
		}
	case *EmbeddingRequest:
		if handlers.Embeddings != nil {
			return handlers.Embeddings(req)
		}
	}

	if handlers.Default != nil {
		return handlers.Default(decoded)
	}

	var zero T
	return zero, fmt.Errorf("unsupported batch item url: %s", decoded.Endpoint)
}

// NormalizeOperationPath returns a stable path-only form for model-facing endpoints.
func NormalizeOperationPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := neturl.Parse(trimmed); err == nil && parsed.Path != "" {
		trimmed = parsed.Path
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

// ResolveBatchItemEndpoint prefers an inline item URL and otherwise falls back to the batch default endpoint.
func ResolveBatchItemEndpoint(defaultEndpoint, itemURL string) string {
	if strings.TrimSpace(itemURL) != "" {
		return itemURL
	}
	return defaultEndpoint
}

// MaybeDecodeKnownBatchItemRequest decodes a batch subrequest when it targets
// one of the requested operations, and reports any other valid item as not
// handled. An item the gateway cannot check (an unsupported url or method, no
// body, or ambiguous JSON) is an error rather than skipped, so it never
// reaches the provider unchecked.
func MaybeDecodeKnownBatchItemRequest(defaultEndpoint string, item BatchRequestItem, operations ...Operation) (*DecodedBatchItemRequest, bool, error) {
	if err := validateBatchItem(defaultEndpoint, item); err != nil {
		return nil, true, err
	}
	endpoint, _ := batchItemEndpoint(ResolveBatchItemEndpoint(defaultEndpoint, item.URL))
	if len(operations) > 0 && !slices.Contains(operations, DescribeEndpointPath(endpoint).Operation) {
		return nil, false, nil
	}
	decoded, err := decodeBatchItem(endpoint, item)
	if err != nil {
		return nil, true, err
	}
	return decoded, true, nil
}

// DecodeKnownBatchItemRequest validates and decodes a batch subrequest.
func DecodeKnownBatchItemRequest(defaultEndpoint string, item BatchRequestItem) (*DecodedBatchItemRequest, error) {
	if err := validateBatchItem(defaultEndpoint, item); err != nil {
		return nil, err
	}
	endpoint, _ := batchItemEndpoint(ResolveBatchItemEndpoint(defaultEndpoint, item.URL))
	return decodeBatchItem(endpoint, item)
}

// decodeBatchItem decodes a validated batch item's body for its canonical
// endpoint.
func decodeBatchItem(endpoint string, item BatchRequestItem) (*DecodedBatchItemRequest, error) {
	decoded := &DecodedBatchItemRequest{
		Endpoint:  endpoint,
		Method:    http.MethodPost,
		Operation: DescribeEndpointPath(endpoint).Operation,
	}
	codec, ok := canonicalOperationCodecFor(decoded.Operation)
	if !ok {
		return nil, fmt.Errorf("unsupported batch item url: %s", endpoint)
	}
	req, err := codec.decodeUncached(item.Body)
	if err != nil {
		return nil, fmt.Errorf("invalid %s request body: %w", strings.ReplaceAll(string(decoded.Operation), "_", " "), err)
	}
	decoded.Request = req
	return decoded, nil
}

// BatchItemRequestedModelSelector derives the raw requested selector for a
// known JSON batch subrequest.
func BatchItemRequestedModelSelector(defaultEndpoint string, item BatchRequestItem) (RequestedModelSelector, error) {
	decoded, err := DecodeKnownBatchItemRequest(defaultEndpoint, item)
	if err != nil {
		return RequestedModelSelector{}, err
	}
	return decoded.RequestedModelSelector()
}
