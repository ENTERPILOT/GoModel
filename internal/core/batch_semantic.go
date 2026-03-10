package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
)

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

// BatchItemModelSelector derives the model selector for a known JSON batch subrequest.
func BatchItemModelSelector(defaultEndpoint string, item BatchRequestItem) (ModelSelector, error) {
	endpoint := NormalizeOperationPath(ResolveBatchItemEndpoint(defaultEndpoint, item.URL))
	if endpoint == "" {
		return ModelSelector{}, fmt.Errorf("url is required")
	}

	method := strings.ToUpper(strings.TrimSpace(item.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost {
		return ModelSelector{}, fmt.Errorf("only POST is supported")
	}
	if len(item.Body) == 0 {
		return ModelSelector{}, fmt.Errorf("body is required")
	}

	switch DescribeEndpointPath(endpoint).Operation {
	case "chat_completions":
		var req ChatRequest
		if err := json.Unmarshal(item.Body, &req); err != nil {
			return ModelSelector{}, fmt.Errorf("invalid chat request body: %w", err)
		}
		return ParseModelSelector(req.Model, req.Provider)
	case "responses":
		var req ResponsesRequest
		if err := json.Unmarshal(item.Body, &req); err != nil {
			return ModelSelector{}, fmt.Errorf("invalid responses request body: %w", err)
		}
		return ParseModelSelector(req.Model, req.Provider)
	case "embeddings":
		var req EmbeddingRequest
		if err := json.Unmarshal(item.Body, &req); err != nil {
			return ModelSelector{}, fmt.Errorf("invalid embeddings request body: %w", err)
		}
		return ParseModelSelector(req.Model, req.Provider)
	default:
		return ModelSelector{}, fmt.Errorf("unsupported batch item url: %s", endpoint)
	}
}
