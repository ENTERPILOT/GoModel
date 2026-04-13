package core

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// RouteHints holds minimal routing-relevant request hints derived from the
// transport snapshot.
//
// These hints are intentionally smaller than a full semantic interpretation.
//
// Lifecycle:
//   - DeriveWhiteBoxPrompt seeds these values directly from transport/body data.
//   - Canonical JSON decode may refine them from a cached request object.
//   - NormalizeModelSelector canonicalizes model/provider values in place.
//
// Consumers that require canonical selector state should prefer a cached canonical
// request or call NormalizeModelSelector before relying on these fields.
type RouteHints struct {
	Model    string
	Provider string
	Endpoint string
}

// PassthroughRouteInfo is typed passthrough metadata derived from ingress
// transport and later enrichment stages.
//
// RawEndpoint reflects the route-relative provider endpoint from the inbound
// path. NormalizedEndpoint, SemanticOperation, and AuditPath are optional and
// may be filled by later gateway enrichment before execution. Once cached on
// WhiteBoxPrompt or Workflow, it should be treated as immutable by later
// request stages.
type PassthroughRouteInfo struct {
	Provider           string
	RawEndpoint        string
	NormalizedEndpoint string
	SemanticOperation  string
	AuditPath          string
	Model              string
}

type semanticCacheKey string

const (
	semanticChatRequestKey      semanticCacheKey = "chat_request"
	semanticResponsesRequestKey semanticCacheKey = "responses_request"
	semanticEmbeddingRequestKey semanticCacheKey = "embedding_request"
	semanticBatchRequestKey     semanticCacheKey = "batch_request"
	semanticBatchMetadataKey    semanticCacheKey = "batch_metadata"
	semanticFileRequestKey      semanticCacheKey = "file_request"
	semanticPassthroughRouteKey semanticCacheKey = "passthrough_route"
)

// WhiteBoxPrompt is the gateway's best-effort semantic extraction from the
// transport snapshot.
// It may be partial and should not be treated as authoritative transport state.
//
// The semantics are populated incrementally:
//   - transport seeds RouteType/OperationType plus sparse RouteHints
//   - route-specific metadata may be cached on demand
//   - canonical request decode may cache a parsed request and refine RouteHints
//   - NormalizeModelSelector may rewrite selector hints into canonical form
type WhiteBoxPrompt struct {
	RouteType     string
	OperationType string
	RouteHints    RouteHints
	// StreamRequested reports that the inbound request explicitly asked for
	// streaming semantics. This is request intent, not endpoint capability.
	StreamRequested bool
	// JSONBodyParsed reports that the captured request body was parsed as JSON
	// (for selector peeking and/or canonical request decode).
	JSONBodyParsed bool

	cache map[semanticCacheKey]any
}

// CachedChatRequest returns the cached canonical chat request, if present.
func (env *WhiteBoxPrompt) CachedChatRequest() *ChatRequest {
	req, _ := cachedSemanticValue[*ChatRequest](env, semanticChatRequestKey)
	return req
}

// CachedResponsesRequest returns the cached canonical responses request, if present.
func (env *WhiteBoxPrompt) CachedResponsesRequest() *ResponsesRequest {
	req, _ := cachedSemanticValue[*ResponsesRequest](env, semanticResponsesRequestKey)
	return req
}

// CachedEmbeddingRequest returns the cached canonical embeddings request, if present.
func (env *WhiteBoxPrompt) CachedEmbeddingRequest() *EmbeddingRequest {
	req, _ := cachedSemanticValue[*EmbeddingRequest](env, semanticEmbeddingRequestKey)
	return req
}

// CachedBatchRequest returns the cached canonical batch create request, if present.
func (env *WhiteBoxPrompt) CachedBatchRequest() *BatchRequest {
	req, _ := cachedSemanticValue[*BatchRequest](env, semanticBatchRequestKey)
	return req
}

// CachedBatchRouteInfo returns cached sparse batch route info, if present.
func (env *WhiteBoxPrompt) CachedBatchRouteInfo() *BatchRouteInfo {
	req, _ := cachedSemanticValue[*BatchRouteInfo](env, semanticBatchMetadataKey)
	return req
}

// CachedFileRouteInfo returns cached sparse file route info, if present.
func (env *WhiteBoxPrompt) CachedFileRouteInfo() *FileRouteInfo {
	req, _ := cachedSemanticValue[*FileRouteInfo](env, semanticFileRequestKey)
	return req
}

// CachedPassthroughRouteInfo returns cached typed passthrough route info, if present.
func (env *WhiteBoxPrompt) CachedPassthroughRouteInfo() *PassthroughRouteInfo {
	req, _ := cachedSemanticValue[*PassthroughRouteInfo](env, semanticPassthroughRouteKey)
	return req
}

// CanonicalSelectorFromCachedRequest returns model/provider selector hints from
// any cached canonical JSON request for the current operation kind.
func (env *WhiteBoxPrompt) CanonicalSelectorFromCachedRequest() (model, provider string, ok bool) {
	if env == nil {
		return "", "", false
	}
	codec, ok := canonicalOperationCodecFor(Operation(env.OperationType))
	if !ok {
		return "", "", false
	}
	req, ok := cachedSemanticAny(env, codec.key)
	if !ok {
		return "", "", false
	}
	return semanticSelectorFromCanonicalRequest(req)
}

func (env *WhiteBoxPrompt) cacheValue(key semanticCacheKey, value any) {
	if env == nil || value == nil {
		return
	}
	if env.cache == nil {
		env.cache = make(map[semanticCacheKey]any, 4)
	}
	env.cache[key] = value
}

func cachedSemanticValue[T any](env *WhiteBoxPrompt, key semanticCacheKey) (T, bool) {
	var zero T
	if env == nil || env.cache == nil {
		return zero, false
	}
	value, ok := env.cache[key]
	if !ok {
		return zero, false
	}
	typed, ok := value.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}

func cachedSemanticAny(env *WhiteBoxPrompt, key semanticCacheKey) (any, bool) {
	if env == nil || env.cache == nil {
		return nil, false
	}
	value, ok := env.cache[key]
	return value, ok
}

func cacheBatchRouteMetadata(env *WhiteBoxPrompt, req *BatchRouteInfo) {
	if env == nil || req == nil {
		return
	}
	env.cacheValue(semanticBatchMetadataKey, req)
}

// CacheFileRouteInfo stores sparse file route metadata on the request semantics.
func CacheFileRouteInfo(env *WhiteBoxPrompt, req *FileRouteInfo) {
	if env == nil || req == nil {
		return
	}
	env.cacheValue(semanticFileRequestKey, req)
	if req.Provider != "" && env.RouteHints.Provider == "" {
		env.RouteHints.Provider = req.Provider
	}
}

// CachePassthroughRouteInfo stores typed passthrough route metadata on the request semantics.
func CachePassthroughRouteInfo(env *WhiteBoxPrompt, req *PassthroughRouteInfo) {
	if env == nil || req == nil {
		return
	}
	env.cacheValue(semanticPassthroughRouteKey, req)
	if req.Provider != "" {
		env.RouteHints.Provider = req.Provider
	}
	if req.RawEndpoint != "" {
		env.RouteHints.Endpoint = req.RawEndpoint
	}
	if req.Model != "" {
		env.RouteHints.Model = req.Model
	}
}

// DeriveWhiteBoxPrompt derives best-effort request semantics from the captured
// transport snapshot.
// Unknown or invalid bodies are tolerated; the returned envelope may be partial.
func DeriveWhiteBoxPrompt(snapshot *RequestSnapshot) *WhiteBoxPrompt {
	if snapshot == nil {
		return nil
	}

	env := &WhiteBoxPrompt{
		RouteHints: RouteHints{
			Endpoint: snapshot.Path,
		},
	}

	desc := DescribeEndpointPath(snapshot.Path)
	if desc.Operation == "" {
		return nil
	}
	env.RouteType = desc.Dialect
	env.OperationType = string(desc.Operation)

	if env.OperationType == string(OperationFiles) {
		CacheFileRouteInfo(env, DeriveFileRouteInfoFromTransport(snapshot.Method, snapshot.Path, snapshot.routeParams, snapshot.queryParams))
	}
	if env.OperationType == string(OperationBatches) {
		cacheBatchRouteMetadata(env, DeriveBatchRouteInfoFromTransport(snapshot.Method, snapshot.Path, snapshot.routeParams, snapshot.queryParams))
	}

	if env.RouteType == "provider_passthrough" {
		CachePassthroughRouteInfo(env, derivePassthroughRouteInfoFromTransport(snapshot))
	}

	if snapshot.capturedBody == nil {
		return env
	}

	trimmed := bytes.TrimSpace(snapshot.capturedBody)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return env
	}

	model, provider, stream, parsed := deriveSnapshotSelectorHintsGJSON(trimmed)
	if !parsed {
		return env
	}
	env.JSONBodyParsed = true

	env.RouteHints.Model = model
	if env.RouteHints.Provider == "" {
		env.RouteHints.Provider = provider
	}
	env.StreamRequested = stream
	if passthrough := env.CachedPassthroughRouteInfo(); passthrough != nil {
		cloned := *passthrough
		if cloned.Provider == "" {
			cloned.Provider = provider
		}
		if model != "" {
			cloned.Model = model
		}
		CachePassthroughRouteInfo(env, &cloned)
	}

	return env
}

func derivePassthroughRouteInfoFromTransport(snapshot *RequestSnapshot) *PassthroughRouteInfo {
	if snapshot == nil {
		return nil
	}
	info := &PassthroughRouteInfo{
		AuditPath: snapshot.Path,
	}
	if provider := snapshot.routeParams["provider"]; provider != "" {
		info.Provider = provider
	}
	if endpoint := snapshot.routeParams["endpoint"]; endpoint != "" {
		info.RawEndpoint = endpoint
	}
	if info.Provider == "" || info.RawEndpoint == "" {
		if provider, endpoint, ok := ParseProviderPassthroughPath(snapshot.Path); ok {
			if info.Provider == "" {
				info.Provider = provider
			}
			if info.RawEndpoint == "" {
				info.RawEndpoint = endpoint
			}
		}
	}
	if info.Provider == "" && info.RawEndpoint == "" && info.AuditPath == "" {
		return nil
	}
	return info
}

func deriveSnapshotSelectorHintsGJSON(body []byte) (model, provider string, stream, parsed bool) {
	if !gjson.ValidBytes(body) {
		return "", "", false, false
	}

	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "", "", false, false
	}

	// gjson returns the first matching top-level field. That differs from
	// encoding/json on duplicate keys, but the hot-path speedup is worth it here:
	// duplicate selector keys are not expected from real clients, and we accept
	// the first-match behavior to keep ingress peeking cheap.
	modelResult := root.Get("model")
	if !snapshotSelectorStringAllowed(modelResult) {
		return "", "", false, false
	}
	providerResult := root.Get("provider")
	if !snapshotSelectorStringAllowed(providerResult) {
		return "", "", false, false
	}
	streamResult := root.Get("stream")
	if !snapshotSelectorBoolAllowed(streamResult) {
		return "", "", false, false
	}

	if modelResult.Type == gjson.String {
		model = modelResult.String()
	}
	if providerResult.Type == gjson.String {
		provider = providerResult.String()
	}
	if streamResult.Type == gjson.True || streamResult.Type == gjson.False {
		stream = streamResult.Bool()
	}
	return model, provider, stream, true
}

func snapshotSelectorStringAllowed(result gjson.Result) bool {
	if !result.Exists() {
		return true
	}
	return result.Type == gjson.String || result.Type == gjson.Null
}

func snapshotSelectorBoolAllowed(result gjson.Result) bool {
	if !result.Exists() {
		return true
	}
	return result.Type == gjson.True || result.Type == gjson.False || result.Type == gjson.Null
}

// DeriveFileRouteInfoFromTransport derives sparse file route info from transport metadata.
func DeriveFileRouteInfoFromTransport(method, path string, routeParams map[string]string, queryParams map[string][]string) *FileRouteInfo {
	req := &FileRouteInfo{
		Action:   fileActionFromTransport(method, path),
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

// DeriveBatchRouteInfoFromTransport derives sparse batch route info from transport metadata.
func DeriveBatchRouteInfoFromTransport(method, path string, routeParams map[string]string, queryParams map[string][]string) *BatchRouteInfo {
	req := &BatchRouteInfo{
		Action:   batchActionFromTransport(method, path),
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

func fileActionFromTransport(method, path string) string {
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

func batchActionFromTransport(method, path string) string {
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
