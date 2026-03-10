package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// SelectorHints holds the minimal routing-relevant request hints derived from ingress.
// These hints are intentionally smaller than a full semantic interpretation.
type SelectorHints struct {
	Model    string
	Provider string
	Endpoint string
}

// SemanticEnvelope is the gateway's best-effort semantic extraction from ingress.
// It may be partial and should not be treated as authoritative transport state.
type SemanticEnvelope struct {
	Dialect          string
	Operation        string
	SelectorHints    SelectorHints
	JSONBodyParsed   bool
	ChatRequest      *ChatRequest
	ResponsesRequest *ResponsesRequest
	EmbeddingRequest *EmbeddingRequest
	BatchRequest     *BatchRequest
	BatchMetadata    *BatchRequestSemantic
	FileRequest      *FileRequestSemantic
}

// BuildSemanticEnvelope derives a best-effort semantic envelope from ingress.
// Unknown or invalid bodies are tolerated; the returned envelope may be partial.
func BuildSemanticEnvelope(frame *IngressFrame) *SemanticEnvelope {
	if frame == nil {
		return nil
	}

	env := &SemanticEnvelope{
		SelectorHints: SelectorHints{
			Endpoint: frame.Path,
		},
	}

	desc := DescribeEndpointPath(frame.Path)
	if desc.Operation == "" {
		return nil
	}
	env.Dialect = desc.Dialect
	env.Operation = desc.Operation

	if env.Operation == "files" {
		env.FileRequest = buildFileRequestSemantic(frame)
		if env.FileRequest != nil && env.SelectorHints.Provider == "" {
			env.SelectorHints.Provider = env.FileRequest.Provider
		}
	}
	if env.Operation == "batches" {
		env.BatchMetadata = buildBatchRequestSemantic(frame)
	}

	if env.Dialect == "provider_passthrough" {
		env.SelectorHints.Endpoint = ""
		if provider := frame.RouteParams["provider"]; provider != "" {
			env.SelectorHints.Provider = provider
		}
		if endpoint := frame.RouteParams["endpoint"]; endpoint != "" {
			env.SelectorHints.Endpoint = endpoint
		}
		if env.SelectorHints.Provider == "" || env.SelectorHints.Endpoint == "" {
			if provider, endpoint, ok := ParseProviderPassthroughPath(frame.Path); ok {
				if env.SelectorHints.Provider == "" {
					env.SelectorHints.Provider = provider
				}
				if env.SelectorHints.Endpoint == "" {
					env.SelectorHints.Endpoint = endpoint
				}
			}
		}
	}

	if frame.RawBody == nil {
		return env
	}

	trimmed := bytes.TrimSpace(frame.RawBody)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return env
	}

	var selectors struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(trimmed, &selectors); err != nil {
		return env
	}
	env.JSONBodyParsed = true

	env.SelectorHints.Model = selectors.Model
	if env.SelectorHints.Provider == "" {
		env.SelectorHints.Provider = selectors.Provider
	}

	return env
}

func buildFileRequestSemantic(frame *IngressFrame) *FileRequestSemantic {
	if frame == nil {
		return nil
	}

	req := &FileRequestSemantic{
		Action:   fileActionFromIngress(frame.Method, frame.Path),
		Provider: firstIngressValue(frame.QueryParams, "provider"),
		Purpose:  firstIngressValue(frame.QueryParams, "purpose"),
		After:    firstIngressValue(frame.QueryParams, "after"),
		LimitRaw: firstIngressValue(frame.QueryParams, "limit"),
		FileID:   fileIDFromIngress(frame),
	}
	if req.LimitRaw != "" {
		if parsed, err := strconv.Atoi(req.LimitRaw); err == nil {
			req.Limit = parsed
			req.HasLimit = true
		}
	}
	if req.Action == "" && req.Provider == "" && req.Purpose == "" && req.After == "" && req.LimitRaw == "" && req.FileID == "" {
		return nil
	}
	return req
}

func buildBatchRequestSemantic(frame *IngressFrame) *BatchRequestSemantic {
	if frame == nil {
		return nil
	}

	req := &BatchRequestSemantic{
		Action:   batchActionFromIngress(frame.Method, frame.Path),
		BatchID:  batchIDFromIngress(frame),
		After:    firstIngressValue(frame.QueryParams, "after"),
		LimitRaw: firstIngressValue(frame.QueryParams, "limit"),
	}
	if req.LimitRaw != "" {
		if parsed, err := strconv.Atoi(req.LimitRaw); err == nil {
			req.Limit = parsed
			req.HasLimit = true
		}
	}
	if req.Action == "" && req.BatchID == "" && req.After == "" && req.LimitRaw == "" {
		return nil
	}
	return req
}

func fileActionFromIngress(method, path string) string {
	switch {
	case path == "/v1/files" && method == http.MethodPost:
		return FileActionCreate
	case path == "/v1/files" && method == http.MethodGet:
		return FileActionList
	case strings.HasSuffix(path, "/content") && method == http.MethodGet:
		return FileActionContent
	case strings.HasPrefix(path, "/v1/files/") && method == http.MethodGet:
		return FileActionGet
	case strings.HasPrefix(path, "/v1/files/") && method == http.MethodDelete:
		return FileActionDelete
	default:
		return ""
	}
}

func fileIDFromIngress(frame *IngressFrame) string {
	if frame == nil {
		return ""
	}
	if id := strings.TrimSpace(frame.RouteParams["id"]); id != "" {
		return id
	}

	trimmed := strings.Trim(strings.TrimSpace(frame.Path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "files" {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

func batchActionFromIngress(method, path string) string {
	switch {
	case path == "/v1/batches" && method == http.MethodPost:
		return BatchActionCreate
	case path == "/v1/batches" && method == http.MethodGet:
		return BatchActionList
	case strings.HasSuffix(path, "/results") && strings.HasPrefix(path, "/v1/batches/") && method == http.MethodGet:
		return BatchActionResults
	case strings.HasSuffix(path, "/cancel") && strings.HasPrefix(path, "/v1/batches/") && method == http.MethodPost:
		return BatchActionCancel
	case strings.HasPrefix(path, "/v1/batches/") && method == http.MethodGet:
		return BatchActionGet
	default:
		return ""
	}
}

func batchIDFromIngress(frame *IngressFrame) string {
	if frame == nil {
		return ""
	}
	if id := strings.TrimSpace(frame.RouteParams["id"]); id != "" {
		return id
	}

	trimmed := strings.Trim(strings.TrimSpace(frame.Path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "batches" {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

func firstIngressValue(values map[string][]string, key string) string {
	if len(values) == 0 {
		return ""
	}
	items, ok := values[key]
	if !ok || len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}
