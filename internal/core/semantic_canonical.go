package core

import (
	"encoding/json"
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

var canonicalSelectorDecoders = map[string]func([]byte, *SemanticEnvelope) (any, error){
	"chat_completions": func(body []byte, env *SemanticEnvelope) (any, error) {
		return DecodeChatRequest(body, env)
	},
	"responses": func(body []byte, env *SemanticEnvelope) (any, error) {
		return DecodeResponsesRequest(body, env)
	},
	"embeddings": func(body []byte, env *SemanticEnvelope) (any, error) {
		return DecodeEmbeddingRequest(body, env)
	},
}

func unmarshalCanonicalJSON[T any](body []byte, newValue func() T) (T, error) {
	req := newValue()
	if err := json.Unmarshal(body, req); err != nil {
		var zero T
		return zero, err
	}
	return req, nil
}

// DecodeChatRequest decodes and caches the canonical chat request for a semantic envelope.
func DecodeChatRequest(body []byte, env *SemanticEnvelope) (*ChatRequest, error) {
	return decodeCanonicalJSON(body, env, canonicalJSONSpec[*ChatRequest]{
		key:      semanticChatRequestKey,
		newValue: func() *ChatRequest { return &ChatRequest{} },
		afterDecode: func(env *SemanticEnvelope, req *ChatRequest) {
			cacheSemanticSelectorHintsFromRequest(env, req)
		},
	})
}

// DecodeResponsesRequest decodes and caches the canonical responses request for a semantic envelope.
func DecodeResponsesRequest(body []byte, env *SemanticEnvelope) (*ResponsesRequest, error) {
	return decodeCanonicalJSON(body, env, canonicalJSONSpec[*ResponsesRequest]{
		key:      semanticResponsesRequestKey,
		newValue: func() *ResponsesRequest { return &ResponsesRequest{} },
		afterDecode: func(env *SemanticEnvelope, req *ResponsesRequest) {
			cacheSemanticSelectorHintsFromRequest(env, req)
		},
	})
}

// DecodeEmbeddingRequest decodes and caches the canonical embeddings request for a semantic envelope.
func DecodeEmbeddingRequest(body []byte, env *SemanticEnvelope) (*EmbeddingRequest, error) {
	return decodeCanonicalJSON(body, env, canonicalJSONSpec[*EmbeddingRequest]{
		key:      semanticEmbeddingRequestKey,
		newValue: func() *EmbeddingRequest { return &EmbeddingRequest{} },
		afterDecode: func(env *SemanticEnvelope, req *EmbeddingRequest) {
			cacheSemanticSelectorHintsFromRequest(env, req)
		},
	})
}

// DecodeBatchRequest decodes and caches the canonical batch request for a semantic envelope.
func DecodeBatchRequest(body []byte, env *SemanticEnvelope) (*BatchRequest, error) {
	return decodeCanonicalJSON(body, env, canonicalJSONSpec[*BatchRequest]{
		key:      semanticBatchRequestKey,
		newValue: func() *BatchRequest { return &BatchRequest{} },
		afterDecode: func(env *SemanticEnvelope, req *BatchRequest) {
			env.JSONBodyParsed = true
		},
	})
}

// BatchRouteMetadata returns sparse batch route semantics, caching them on the envelope when present.
func BatchRouteMetadata(env *SemanticEnvelope, method, path string, routeParams map[string]string, queryParams map[string][]string) (*BatchRequestSemantic, error) {
	req := (*BatchRequestSemantic)(nil)
	if env != nil {
		req = env.CachedBatchMetadata()
	}
	if req == nil {
		req = BuildBatchRequestSemanticFromTransport(method, path, routeParams, queryParams)
		if req == nil {
			req = &BatchRequestSemantic{}
		}
	}

	if req.LimitRaw != "" && !req.HasLimit {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.LimitRaw))
		if err != nil {
			return nil, NewInvalidRequestError("invalid limit parameter", err)
		}
		req.Limit = parsed
		req.HasLimit = true
	}
	cacheBatchRouteMetadata(env, req)
	return req, nil
}

// FileRouteMetadata returns sparse file route semantics, caching them on the envelope when present.
func FileRouteMetadata(env *SemanticEnvelope, method, path string, routeParams map[string]string, queryParams map[string][]string) (*FileRequestSemantic, error) {
	req := (*FileRequestSemantic)(nil)
	if env != nil {
		req = env.CachedFileRequest()
	}
	if req == nil {
		req = BuildFileRequestSemanticFromTransport(method, path, routeParams, queryParams)
		if req == nil {
			req = &FileRequestSemantic{}
		}
	}

	if req.LimitRaw != "" && !req.HasLimit {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.LimitRaw))
		if err != nil {
			return nil, NewInvalidRequestError("invalid limit parameter", err)
		}
		req.Limit = parsed
		req.HasLimit = true
	}
	CacheFileRequestSemantic(env, req)
	return req, nil
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
	decode, ok := canonicalSelectorDecoders[env.Operation]
	if !ok {
		return "", "", false
	}
	req, err := decode(body, env)
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
