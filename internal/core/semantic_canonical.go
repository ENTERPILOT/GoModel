package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type canonicalJSONSpec[T any] struct {
	key         semanticCacheKey
	newValue    func() T
	afterDecode func(*SemanticEnvelope, T)
}

type semanticSelectorCarrier interface {
	semanticSelector() (string, string)
}

type canonicalOperationCodec struct {
	key            semanticCacheKey
	decode         func([]byte, *SemanticEnvelope) (any, error)
	decodeUncached func([]byte) (any, error)
}

func unmarshalCanonicalJSON[T any](body []byte, newValue func() T) (T, error) {
	req := newValue()
	if err := json.Unmarshal(body, req); err != nil {
		var zero T
		return zero, err
	}
	return req, nil
}

func newCanonicalOperationCodec[T any](key semanticCacheKey, newValue func() T, afterDecode func(*SemanticEnvelope, T)) canonicalOperationCodec {
	return canonicalOperationCodec{
		key: key,
		decode: func(body []byte, env *SemanticEnvelope) (any, error) {
			return decodeCanonicalJSON(body, env, canonicalJSONSpec[T]{
				key:         key,
				newValue:    newValue,
				afterDecode: afterDecode,
			})
		},
		decodeUncached: func(body []byte) (any, error) {
			return unmarshalCanonicalJSON(body, newValue)
		},
	}
}

var canonicalOperationCodecs = map[string]canonicalOperationCodec{
	"chat_completions": newCanonicalOperationCodec(semanticChatRequestKey, func() *ChatRequest { return &ChatRequest{} }, func(env *SemanticEnvelope, req *ChatRequest) {
		cacheSemanticSelectorHintsFromRequest(env, req)
	}),
	"responses": newCanonicalOperationCodec(semanticResponsesRequestKey, func() *ResponsesRequest { return &ResponsesRequest{} }, func(env *SemanticEnvelope, req *ResponsesRequest) {
		cacheSemanticSelectorHintsFromRequest(env, req)
	}),
	"embeddings": newCanonicalOperationCodec(semanticEmbeddingRequestKey, func() *EmbeddingRequest { return &EmbeddingRequest{} }, func(env *SemanticEnvelope, req *EmbeddingRequest) {
		cacheSemanticSelectorHintsFromRequest(env, req)
	}),
	"batches": newCanonicalOperationCodec(semanticBatchRequestKey, func() *BatchRequest { return &BatchRequest{} }, func(env *SemanticEnvelope, req *BatchRequest) {
		env.JSONBodyParsed = true
	}),
}

func canonicalOperationCodecFor(operation string) (canonicalOperationCodec, bool) {
	codec, ok := canonicalOperationCodecs[operation]
	return codec, ok
}

func decodeCanonicalOperation[T any](body []byte, env *SemanticEnvelope, operation string) (T, error) {
	codec, ok := canonicalOperationCodecFor(operation)
	if !ok {
		var zero T
		return zero, fmt.Errorf("unsupported canonical operation: %s", operation)
	}
	decoded, err := codec.decode(body, env)
	if err != nil {
		var zero T
		return zero, err
	}
	typed, ok := decoded.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("unexpected canonical request type for operation: %s", operation)
	}
	return typed, nil
}

// DecodeChatRequest decodes and caches the canonical chat request for a semantic envelope.
func DecodeChatRequest(body []byte, env *SemanticEnvelope) (*ChatRequest, error) {
	return decodeCanonicalOperation[*ChatRequest](body, env, "chat_completions")
}

// DecodeResponsesRequest decodes and caches the canonical responses request for a semantic envelope.
func DecodeResponsesRequest(body []byte, env *SemanticEnvelope) (*ResponsesRequest, error) {
	return decodeCanonicalOperation[*ResponsesRequest](body, env, "responses")
}

// DecodeEmbeddingRequest decodes and caches the canonical embeddings request for a semantic envelope.
func DecodeEmbeddingRequest(body []byte, env *SemanticEnvelope) (*EmbeddingRequest, error) {
	return decodeCanonicalOperation[*EmbeddingRequest](body, env, "embeddings")
}

// DecodeBatchRequest decodes and caches the canonical batch request for a semantic envelope.
func DecodeBatchRequest(body []byte, env *SemanticEnvelope) (*BatchRequest, error) {
	return decodeCanonicalOperation[*BatchRequest](body, env, "batches")
}

func parseRouteLimit(limitRaw string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(limitRaw))
	if err != nil {
		return 0, NewInvalidRequestError("invalid limit parameter", err)
	}
	return parsed, nil
}

func cachedRouteMetadata[T any](
	env *SemanticEnvelope,
	cached func(*SemanticEnvelope) *T,
	build func() *T,
	applyLimit func(*T) error,
	store func(*SemanticEnvelope, *T),
) (*T, error) {
	req := (*T)(nil)
	if env != nil {
		req = cached(env)
	}
	if req == nil {
		req = build()
		if req == nil {
			req = new(T)
		}
	}
	if err := applyLimit(req); err != nil {
		return nil, err
	}
	store(env, req)
	return req, nil
}

// BatchRouteMetadata returns sparse batch route semantics, caching them on the envelope when present.
func BatchRouteMetadata(env *SemanticEnvelope, method, path string, routeParams map[string]string, queryParams map[string][]string) (*BatchRequestSemantic, error) {
	return cachedRouteMetadata(
		env,
		func(env *SemanticEnvelope) *BatchRequestSemantic {
			return env.CachedBatchMetadata()
		},
		func() *BatchRequestSemantic {
			return BuildBatchRequestSemanticFromTransport(method, path, routeParams, queryParams)
		},
		(*BatchRequestSemantic).ensureParsedLimit,
		cacheBatchRouteMetadata,
	)
}

// FileRouteMetadata returns sparse file route semantics, caching them on the envelope when present.
func FileRouteMetadata(env *SemanticEnvelope, method, path string, routeParams map[string]string, queryParams map[string][]string) (*FileRequestSemantic, error) {
	return cachedRouteMetadata(
		env,
		func(env *SemanticEnvelope) *FileRequestSemantic {
			return env.CachedFileRequest()
		},
		func() *FileRequestSemantic {
			return BuildFileRequestSemanticFromTransport(method, path, routeParams, queryParams)
		},
		(*FileRequestSemantic).ensureParsedLimit,
		CacheFileRequestSemantic,
	)
}

// NormalizeModelSelector canonicalizes model/provider selector inputs and keeps
// semantic selector hints aligned with the normalized request state.
func NormalizeModelSelector(env *SemanticEnvelope, model, provider *string) error {
	if model == nil || provider == nil {
		return NewInvalidRequestError("model selector targets are required", nil)
	}

	selector, err := ParseModelSelector(*model, *provider)
	if err != nil {
		return NewInvalidRequestError(err.Error(), err)
	}

	*model = selector.Model
	*provider = selector.Provider

	if env != nil {
		env.SelectorHints.Model = selector.Model
		env.SelectorHints.Provider = selector.Provider
	}
	return nil
}

func DecodeCanonicalSelector(body []byte, env *SemanticEnvelope) (model, provider string, ok bool) {
	if env == nil {
		return "", "", false
	}
	codec, ok := canonicalOperationCodecFor(env.Operation)
	if !ok {
		return "", "", false
	}
	req, err := codec.decode(body, env)
	if err != nil {
		return "", "", false
	}
	return semanticSelectorFromCanonicalRequest(req)
}

func decodeCanonicalJSON[T any](body []byte, env *SemanticEnvelope, spec canonicalJSONSpec[T]) (T, error) {
	if req, ok := cachedSemanticValue[T](env, spec.key); ok {
		return req, nil
	}

	req, err := unmarshalCanonicalJSON(body, spec.newValue)
	if err != nil {
		var zero T
		return zero, err
	}
	if env != nil {
		env.cacheValue(spec.key, req)
		if spec.afterDecode != nil {
			spec.afterDecode(env, req)
		}
	}
	return req, nil
}

func cacheSemanticSelectorHints(env *SemanticEnvelope, model, provider string) {
	if env == nil {
		return
	}
	env.JSONBodyParsed = true
	env.SelectorHints.Model = model
	if env.SelectorHints.Provider == "" {
		env.SelectorHints.Provider = provider
	}
}

func cacheSemanticSelectorHintsFromRequest(env *SemanticEnvelope, req any) {
	model, provider, ok := semanticSelectorFromCanonicalRequest(req)
	if !ok {
		return
	}
	cacheSemanticSelectorHints(env, model, provider)
}

func semanticSelectorFromCanonicalRequest(req any) (model, provider string, ok bool) {
	carrier, ok := req.(semanticSelectorCarrier)
	if !ok || carrier == nil {
		return "", "", false
	}
	model, provider = carrier.semanticSelector()
	return model, provider, true
}
