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
	afterDecode func(*WhiteBoxPrompt, T)
}

type semanticSelectorCarrier interface {
	semanticSelector() (string, string)
}

type canonicalOperationCodec struct {
	key            semanticCacheKey
	decode         func([]byte, *WhiteBoxPrompt) (any, error)
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

func newCanonicalOperationCodec[T any](key semanticCacheKey, newValue func() T, afterDecode func(*WhiteBoxPrompt, T)) canonicalOperationCodec {
	return canonicalOperationCodec{
		key: key,
		decode: func(body []byte, env *WhiteBoxPrompt) (any, error) {
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
	"chat_completions": newCanonicalOperationCodec(semanticChatRequestKey, func() *ChatRequest { return &ChatRequest{} }, func(env *WhiteBoxPrompt, req *ChatRequest) {
		cacheSemanticSelectorHintsFromRequest(env, req)
	}),
	"responses": newCanonicalOperationCodec(semanticResponsesRequestKey, func() *ResponsesRequest { return &ResponsesRequest{} }, func(env *WhiteBoxPrompt, req *ResponsesRequest) {
		cacheSemanticSelectorHintsFromRequest(env, req)
	}),
	"embeddings": newCanonicalOperationCodec(semanticEmbeddingRequestKey, func() *EmbeddingRequest { return &EmbeddingRequest{} }, func(env *WhiteBoxPrompt, req *EmbeddingRequest) {
		cacheSemanticSelectorHintsFromRequest(env, req)
	}),
	"batches": newCanonicalOperationCodec(semanticBatchRequestKey, func() *BatchRequest { return &BatchRequest{} }, func(env *WhiteBoxPrompt, req *BatchRequest) {
		env.JSONBodyParsed = true
	}),
}

func canonicalOperationCodecFor(operation string) (canonicalOperationCodec, bool) {
	codec, ok := canonicalOperationCodecs[operation]
	return codec, ok
}

func decodeCanonicalOperation[T any](body []byte, env *WhiteBoxPrompt, operation string) (T, error) {
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
func DecodeChatRequest(body []byte, env *WhiteBoxPrompt) (*ChatRequest, error) {
	return decodeCanonicalOperation[*ChatRequest](body, env, "chat_completions")
}

// DecodeResponsesRequest decodes and caches the canonical responses request for a semantic envelope.
func DecodeResponsesRequest(body []byte, env *WhiteBoxPrompt) (*ResponsesRequest, error) {
	return decodeCanonicalOperation[*ResponsesRequest](body, env, "responses")
}

// DecodeEmbeddingRequest decodes and caches the canonical embeddings request for a semantic envelope.
func DecodeEmbeddingRequest(body []byte, env *WhiteBoxPrompt) (*EmbeddingRequest, error) {
	return decodeCanonicalOperation[*EmbeddingRequest](body, env, "embeddings")
}

// DecodeBatchRequest decodes and caches the canonical batch request for a semantic envelope.
func DecodeBatchRequest(body []byte, env *WhiteBoxPrompt) (*BatchRequest, error) {
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
	env *WhiteBoxPrompt,
	cached func(*WhiteBoxPrompt) *T,
	build func() *T,
	applyLimit func(*T) error,
	store func(*WhiteBoxPrompt, *T),
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
func BatchRouteMetadata(env *WhiteBoxPrompt, method, path string, routeParams map[string]string, queryParams map[string][]string) (*BatchRouteInfo, error) {
	return cachedRouteMetadata(
		env,
		func(env *WhiteBoxPrompt) *BatchRouteInfo {
			return env.CachedBatchRouteInfo()
		},
		func() *BatchRouteInfo {
			return DeriveBatchRouteInfoFromTransport(method, path, routeParams, queryParams)
		},
		(*BatchRouteInfo).ensureParsedLimit,
		cacheBatchRouteMetadata,
	)
}

// FileRouteMetadata returns sparse file route semantics, caching them on the envelope when present.
func FileRouteMetadata(env *WhiteBoxPrompt, method, path string, routeParams map[string]string, queryParams map[string][]string) (*FileRouteInfo, error) {
	return cachedRouteMetadata(
		env,
		func(env *WhiteBoxPrompt) *FileRouteInfo {
			return env.CachedFileRouteInfo()
		},
		func() *FileRouteInfo {
			return DeriveFileRouteInfoFromTransport(method, path, routeParams, queryParams)
		},
		(*FileRouteInfo).ensureParsedLimit,
		CacheFileRouteInfo,
	)
}

// NormalizeModelSelector canonicalizes model/provider selector inputs and keeps
// semantic selector hints aligned with the normalized request state.
//
// This is the point where RouteHints transition from raw ingress values
// (which may still contain a qualified model string like "openai/gpt-5-mini")
// to canonical model/provider fields.
func NormalizeModelSelector(env *WhiteBoxPrompt, model, provider *string) error {
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
		env.RouteHints.Model = selector.Model
		env.RouteHints.Provider = selector.Provider
	}
	return nil
}

// DecodeCanonicalSelector decodes a canonical request body using the codec
// resolved by canonicalOperationCodecFor for env, then extracts the model and
// provider via semanticSelectorFromCanonicalRequest. It returns ok=false for a
// nil env, missing codec, or decode failure.
func DecodeCanonicalSelector(body []byte, env *WhiteBoxPrompt) (model, provider string, ok bool) {
	if env == nil {
		return "", "", false
	}
	codec, ok := canonicalOperationCodecFor(env.OperationType)
	if !ok {
		return "", "", false
	}
	req, err := codec.decode(body, env)
	if err != nil {
		return "", "", false
	}
	return semanticSelectorFromCanonicalRequest(req)
}

func decodeCanonicalJSON[T any](body []byte, env *WhiteBoxPrompt, spec canonicalJSONSpec[T]) (T, error) {
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

func cacheSemanticSelectorHints(env *WhiteBoxPrompt, model, provider string) {
	if env == nil {
		return
	}
	env.JSONBodyParsed = true
	env.RouteHints.Model = model
	if env.RouteHints.Provider == "" {
		env.RouteHints.Provider = provider
	}
}

func cacheSemanticSelectorHintsFromRequest(env *WhiteBoxPrompt, req any) {
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
