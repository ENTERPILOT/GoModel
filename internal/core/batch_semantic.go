package core

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
)

var knownBatchItemDecoders = map[string]func([]byte) (any, error){
	"chat_completions": func(body []byte) (any, error) {
		return unmarshalCanonicalJSON(body, func() *ChatRequest { return &ChatRequest{} })
	},
	"responses": func(body []byte) (any, error) {
		return unmarshalCanonicalJSON(body, func() *ResponsesRequest { return &ResponsesRequest{} })
	},
	"embeddings": func(body []byte) (any, error) {
		return unmarshalCanonicalJSON(body, func() *EmbeddingRequest { return &EmbeddingRequest{} })
	},
}

// DecodedBatchItemRequest is the canonical decode result for known JSON batch subrequests.
type DecodedBatchItemRequest struct {
	Endpoint  string
	Method    string
	Operation string
	Request   any
}

func (decoded *DecodedBatchItemRequest) ChatRequest() *ChatRequest {
	if decoded == nil {
		return nil
	}
	req, _ := decoded.Request.(*ChatRequest)
	return req
}

func (decoded *DecodedBatchItemRequest) ResponsesRequest() *ResponsesRequest {
	if decoded == nil {
		return nil
	}
	req, _ := decoded.Request.(*ResponsesRequest)
	return req
}

func (decoded *DecodedBatchItemRequest) EmbeddingRequest() *EmbeddingRequest {
	if decoded == nil {
		return nil
	}
	req, _ := decoded.Request.(*EmbeddingRequest)
	return req
}

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

	decode, ok := knownBatchItemDecoders[decoded.Operation]
	if !ok {
		return nil, fmt.Errorf("unsupported batch item url: %s", endpoint)
	}
	req, err := decode(item.Body)
	if err != nil {
		return nil, fmt.Errorf("invalid %s request body: %w", strings.ReplaceAll(decoded.Operation, "_", " "), err)
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
