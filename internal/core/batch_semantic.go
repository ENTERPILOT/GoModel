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

// ChatRequest returns the decoded ChatRequest when the receiver is non-nil and
// the underlying Request is a *ChatRequest. It returns nil for a nil receiver
// or for non-chat batch items.
func (decoded *DecodedBatchItemRequest) ChatRequest() *ChatRequest {
	if decoded == nil {
		return nil
	}
	req, _ := decoded.Request.(*ChatRequest)
	return req
}

// ResponsesRequest returns the decoded ResponsesRequest when the receiver is
// non-nil and the underlying Request is a *ResponsesRequest. It returns nil for
// a nil receiver or for non-responses batch items.
func (decoded *DecodedBatchItemRequest) ResponsesRequest() *ResponsesRequest {
	if decoded == nil {
		return nil
	}
	req, _ := decoded.Request.(*ResponsesRequest)
	return req
}

// EmbeddingRequest returns the decoded EmbeddingRequest when the receiver is
// non-nil and the underlying Request is an *EmbeddingRequest. It returns nil
// for a nil receiver or for non-embedding batch items.
func (decoded *DecodedBatchItemRequest) EmbeddingRequest() *EmbeddingRequest {
	if decoded == nil {
		return nil
	}
	req, _ := decoded.Request.(*EmbeddingRequest)
	return req
}

// ModelSelector returns the selected model/provider pair for the decoded batch
// item. It returns an error when the receiver is nil, when the decoded request
// type is unsupported, or when the canonical selector cannot be parsed.
func (decoded *DecodedBatchItemRequest) ModelSelector() (ModelSelector, error) {
	if decoded == nil {
		return ModelSelector{}, fmt.Errorf("decoded batch request is required")
	}
	model, provider, ok := semanticSelectorFromCanonicalRequest(decoded.Request)
	if !ok {
		return ModelSelector{}, fmt.Errorf("unsupported batch item url: %s", decoded.Endpoint)
	}
	return ParseModelSelector(model, provider)
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

// MaybeDecodeKnownBatchItemRequest selectively decodes a known JSON batch
// subrequest only when it targets one of the requested operations. Non-POST,
// body-less, or unmatched items are reported as not handled.
func MaybeDecodeKnownBatchItemRequest(defaultEndpoint string, item BatchRequestItem, operations ...Operation) (*DecodedBatchItemRequest, bool, error) {
	if len(item.Body) == 0 {
		return nil, false, nil
	}

	method := strings.ToUpper(strings.TrimSpace(item.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost {
		return nil, false, nil
	}

	endpoint := NormalizeOperationPath(ResolveBatchItemEndpoint(defaultEndpoint, item.URL))
	if endpoint == "" {
		return nil, false, nil
	}
	operation := DescribeEndpointPath(endpoint).Operation
	if len(operations) > 0 && !slices.Contains(operations, operation) {
		return nil, false, nil
	}

	decoded, err := DecodeKnownBatchItemRequest(defaultEndpoint, item)
	if err != nil {
		return nil, true, err
	}
	return decoded, true, nil
}

// DecodeKnownBatchItemRequest normalizes and decodes a known JSON batch subrequest.
func DecodeKnownBatchItemRequest(defaultEndpoint string, item BatchRequestItem) (*DecodedBatchItemRequest, error) {
	endpoint := NormalizeOperationPath(ResolveBatchItemEndpoint(defaultEndpoint, item.URL))
	if endpoint == "" {
		return nil, fmt.Errorf("url is required")
	}

	method := strings.ToUpper(strings.TrimSpace(item.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost {
		return nil, fmt.Errorf("only POST is supported")
	}
	if len(item.Body) == 0 {
		return nil, fmt.Errorf("body is required")
	}

	decoded := &DecodedBatchItemRequest{
		Endpoint:  endpoint,
		Method:    method,
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

// BatchItemModelSelector derives the model selector for a known JSON batch subrequest.
func BatchItemModelSelector(defaultEndpoint string, item BatchRequestItem) (ModelSelector, error) {
	decoded, err := DecodeKnownBatchItemRequest(defaultEndpoint, item)
	if err != nil {
		return ModelSelector{}, err
	}
	return decoded.ModelSelector()
}
