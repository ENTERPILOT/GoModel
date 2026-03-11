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
//
// Lifecycle:
//   - BuildSemanticEnvelope seeds these values directly from ingress transport/body data.
//   - Canonical JSON decode may refine them from a cached request object.
//   - NormalizeModelSelector canonicalizes model/provider values in place.
//
// Consumers that require canonical selector state should prefer a cached canonical
// request or call NormalizeModelSelector before relying on these fields.
type SelectorHints struct {
	Model    string
	Provider string
	Endpoint string
}

type semanticCacheKey string

const (
	semanticChatRequestKey      semanticCacheKey = "chat_request"
	semanticResponsesRequestKey semanticCacheKey = "responses_request"
	semanticEmbeddingRequestKey semanticCacheKey = "embedding_request"
	semanticBatchRequestKey     semanticCacheKey = "batch_request"
	semanticBatchMetadataKey    semanticCacheKey = "batch_metadata"
	semanticFileRequestKey      semanticCacheKey = "file_request"
)

// SemanticEnvelope is the gateway's best-effort semantic extraction from ingress.
// It may be partial and should not be treated as authoritative transport state.
//
// The envelope is populated incrementally:
//   - ingress seeds Dialect/Operation plus sparse SelectorHints
//   - route-specific metadata may be cached on demand
//   - canonical request decode may cache a parsed request and refine SelectorHints
//   - NormalizeModelSelector may rewrite selector hints into canonical form
type SemanticEnvelope struct {
	Dialect        string
	Operation      string
	SelectorHints  SelectorHints
	JSONBodyParsed bool

	cachedValues map[semanticCacheKey]any
}

// CachedChatRequest returns the cached canonical chat request, if present.
func (env *SemanticEnvelope) CachedChatRequest() *ChatRequest {
	req, _ := cachedSemanticValue[*ChatRequest](env, semanticChatRequestKey)
	return req
}

// CachedResponsesRequest returns the cached canonical responses request, if present.
func (env *SemanticEnvelope) CachedResponsesRequest() *ResponsesRequest {
	req, _ := cachedSemanticValue[*ResponsesRequest](env, semanticResponsesRequestKey)
	return req
}

// CachedEmbeddingRequest returns the cached canonical embeddings request, if present.
func (env *SemanticEnvelope) CachedEmbeddingRequest() *EmbeddingRequest {
	req, _ := cachedSemanticValue[*EmbeddingRequest](env, semanticEmbeddingRequestKey)
	return req
}

// CachedBatchRequest returns the cached canonical batch create request, if present.
func (env *SemanticEnvelope) CachedBatchRequest() *BatchRequest {
	req, _ := cachedSemanticValue[*BatchRequest](env, semanticBatchRequestKey)
	return req
}

// CachedBatchMetadata returns the cached sparse batch route metadata, if present.
func (env *SemanticEnvelope) CachedBatchMetadata() *BatchRequestSemantic {
	req, _ := cachedSemanticValue[*BatchRequestSemantic](env, semanticBatchMetadataKey)
	return req
}

// CachedFileRequest returns the cached sparse file route metadata, if present.
func (env *SemanticEnvelope) CachedFileRequest() *FileRequestSemantic {
	req, _ := cachedSemanticValue[*FileRequestSemantic](env, semanticFileRequestKey)
	return req
}

// CachedCanonicalSelector returns model/provider selector hints from any cached
// canonical JSON request for the current operation.
func (env *SemanticEnvelope) CachedCanonicalSelector() (model, provider string, ok bool) {
	if env == nil {
		return "", "", false
	}
	codec, ok := canonicalOperationCodecFor(env.Operation)
	if !ok {
		return "", "", false
	}
	req, ok := cachedSemanticAny(env, codec.key)
	if !ok {
		return "", "", false
	}
	return semanticSelectorFromCanonicalRequest(req)
}

func (env *SemanticEnvelope) cacheValue(key semanticCacheKey, value any) {
	if env == nil || value == nil {
		return
	}
	if env.cachedValues == nil {
		env.cachedValues = make(map[semanticCacheKey]any, 4)
	}
	env.cachedValues[key] = value
}

func cachedSemanticValue[T any](env *SemanticEnvelope, key semanticCacheKey) (T, bool) {
	var zero T
	if env == nil || env.cachedValues == nil {
		return zero, false
	}
	value, ok := env.cachedValues[key]
	if !ok {
		return zero, false
	}
	typed, ok := value.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}

func cachedSemanticAny(env *SemanticEnvelope, key semanticCacheKey) (any, bool) {
	if env == nil || env.cachedValues == nil {
		return nil, false
	}
	value, ok := env.cachedValues[key]
	return value, ok
}

func cacheBatchRouteMetadata(env *SemanticEnvelope, req *BatchRequestSemantic) {
	if env == nil || req == nil {
		return
	}
	env.cacheValue(semanticBatchMetadataKey, req)
}

// CacheFileRequestSemantic stores sparse file route metadata on the semantic envelope.
func CacheFileRequestSemantic(env *SemanticEnvelope, req *FileRequestSemantic) {
	if env == nil || req == nil {
		return
	}
	env.cacheValue(semanticFileRequestKey, req)
	if req.Provider != "" && env.SelectorHints.Provider == "" {
		env.SelectorHints.Provider = req.Provider
	}
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
		CacheFileRequestSemantic(env, BuildFileRequestSemanticFromTransport(frame.Method, frame.Path, frame.RouteParams, frame.QueryParams))
	}
	if env.Operation == "batches" {
		cacheBatchRouteMetadata(env, BuildBatchRequestSemanticFromTransport(frame.Method, frame.Path, frame.RouteParams, frame.QueryParams))
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

// BuildFileRequestSemanticFromTransport derives sparse file semantics from transport metadata.
func BuildFileRequestSemanticFromTransport(method, path string, routeParams map[string]string, queryParams map[string][]string) *FileRequestSemantic {
	req := &FileRequestSemantic{
		Action:   fileActionFromIngress(method, path),
		Provider: firstTransportValue(queryParams, "provider"),
		Purpose:  firstTransportValue(queryParams, "purpose"),
		After:    firstTransportValue(queryParams, "after"),
		LimitRaw: firstTransportValue(queryParams, "limit"),
		FileID:   fileIDFromTransport(path, routeParams),
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

// BuildBatchRequestSemanticFromTransport derives sparse batch route semantics from transport metadata.
func BuildBatchRequestSemanticFromTransport(method, path string, routeParams map[string]string, queryParams map[string][]string) *BatchRequestSemantic {
	req := &BatchRequestSemantic{
		Action:   batchActionFromIngress(method, path),
		BatchID:  batchIDFromTransport(path, routeParams),
		After:    firstTransportValue(queryParams, "after"),
		LimitRaw: firstTransportValue(queryParams, "limit"),
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

func fileIDFromTransport(path string, routeParams map[string]string) string {
	if id := strings.TrimSpace(routeParams["id"]); id != "" {
		return id
	}

	trimmed := strings.Trim(strings.TrimSpace(path), "/")
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

func batchIDFromTransport(path string, routeParams map[string]string) string {
	if id := strings.TrimSpace(routeParams["id"]); id != "" {
		return id
	}

	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "batches" {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

func firstTransportValue(values map[string][]string, key string) string {
	if len(values) == 0 {
		return ""
	}
	items, ok := values[key]
	if !ok || len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}
