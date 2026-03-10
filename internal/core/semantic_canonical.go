package core

import (
	"encoding/json"
	"strconv"
	"strings"
)

// DecodeChatRequest decodes and caches the canonical chat request for a semantic envelope.
func DecodeChatRequest(body []byte, env *SemanticEnvelope) (*ChatRequest, error) {
	return decodeCanonicalJSON(body, env,
		func(env *SemanticEnvelope) *ChatRequest {
			return env.ChatRequest
		},
		func(env *SemanticEnvelope, req *ChatRequest) {
			env.ChatRequest = req
			cacheSemanticSelectorHints(env, req.Model, req.Provider)
		},
	)
}

// DecodeResponsesRequest decodes and caches the canonical responses request for a semantic envelope.
func DecodeResponsesRequest(body []byte, env *SemanticEnvelope) (*ResponsesRequest, error) {
	return decodeCanonicalJSON(body, env,
		func(env *SemanticEnvelope) *ResponsesRequest {
			return env.ResponsesRequest
		},
		func(env *SemanticEnvelope, req *ResponsesRequest) {
			env.ResponsesRequest = req
			cacheSemanticSelectorHints(env, req.Model, req.Provider)
		},
	)
}

// DecodeEmbeddingRequest decodes and caches the canonical embeddings request for a semantic envelope.
func DecodeEmbeddingRequest(body []byte, env *SemanticEnvelope) (*EmbeddingRequest, error) {
	return decodeCanonicalJSON(body, env,
		func(env *SemanticEnvelope) *EmbeddingRequest {
			return env.EmbeddingRequest
		},
		func(env *SemanticEnvelope, req *EmbeddingRequest) {
			env.EmbeddingRequest = req
			cacheSemanticSelectorHints(env, req.Model, req.Provider)
		},
	)
}

// DecodeBatchRequest decodes and caches the canonical batch request for a semantic envelope.
func DecodeBatchRequest(body []byte, env *SemanticEnvelope) (*BatchRequest, error) {
	return decodeCanonicalJSON(body, env,
		func(env *SemanticEnvelope) *BatchRequest {
			return env.BatchRequest
		},
		func(env *SemanticEnvelope, req *BatchRequest) {
			env.BatchRequest = req
			env.JSONBodyParsed = true
		},
	)
}

// BatchRouteMetadata returns sparse batch route semantics, caching them on the envelope when present.
func BatchRouteMetadata(env *SemanticEnvelope, method, path string, routeParams map[string]string, queryParams map[string][]string) (*BatchRequestSemantic, error) {
	var req *BatchRequestSemantic
	if env != nil && env.BatchMetadata != nil {
		req = env.BatchMetadata
	} else {
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
	if env != nil {
		env.BatchMetadata = req
	}
	return req, nil
}

// FileRouteMetadata returns sparse file route semantics, caching them on the envelope when present.
func FileRouteMetadata(env *SemanticEnvelope, method, path string, routeParams map[string]string, queryParams map[string][]string) (*FileRequestSemantic, error) {
	var req *FileRequestSemantic
	if env != nil && env.FileRequest != nil {
		req = env.FileRequest
	} else {
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
	if env != nil {
		env.FileRequest = req
		if req.Provider != "" && env.SelectorHints.Provider == "" {
			env.SelectorHints.Provider = req.Provider
		}
	}
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

func decodeCanonicalJSON[T any](
	body []byte,
	env *SemanticEnvelope,
	current func(*SemanticEnvelope) *T,
	cache func(*SemanticEnvelope, *T),
) (*T, error) {
	if env != nil {
		if req := current(env); req != nil {
			return req, nil
		}
	}

	req := new(T)
	if err := json.Unmarshal(body, req); err != nil {
		return nil, err
	}
	if env != nil {
		cache(env, req)
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
