package providers

import (
	"context"
	"io"

	"github.com/enterpilot/gomodel/internal/core"
)

// Provider is gateway routing metadata on OpenAI-compatible request structs and
// must be removed before dispatching to an upstream provider implementation.
// Replay state another provider owns (extra_content) is dropped for every
// dialect; Anthropic cache directives only reach providers that accept them.
func forwardChatRequest(ctx context.Context, req *core.ChatRequest, route resolvedRoute) *core.ChatRequest {
	forwardReq := *req
	forwardReq.Model = route.selector.Model
	forwardReq.Provider = ""
	forward := adaptExtraContent(&forwardReq, route.providerType)
	if core.RequestDialectFromContext(ctx) != core.RequestDialectAnthropicMessages {
		return forward
	}
	return adaptAnthropicCacheControl(forward, route.providerType)
}

func forwardResponsesRequest(req *core.ResponsesRequest, route resolvedRoute) *core.ResponsesRequest {
	forwardReq := *req
	forwardReq.Model = route.selector.Model
	forwardReq.Provider = ""
	return adaptResponsesExtraContent(&forwardReq, route.providerType)
}

func (r *Router) plannedChatRequest(ctx context.Context, req *core.ChatRequest, route resolvedRoute) *core.ChatRequest {
	forward := forwardChatRequest(ctx, req, route)
	if r.cachePlanner == nil {
		return forward
	}
	return r.cachePlanner.planChat(forward, route.providerType, route.selector)
}

func (r *Router) plannedResponsesRequest(req *core.ResponsesRequest, route resolvedRoute) *core.ResponsesRequest {
	forward := forwardResponsesRequest(req, route)
	if r.cachePlanner == nil {
		return forward
	}
	return r.cachePlanner.planResponses(forward, route.providerType, route.selector)
}

func forwardEmbeddingRequest(req *core.EmbeddingRequest, selector core.ModelSelector) *core.EmbeddingRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func callChatCompletion(ctx context.Context, provider core.Provider, req *core.ChatRequest) (*core.ChatResponse, error) {
	return provider.ChatCompletion(ctx, req)
}

func callResponses(ctx context.Context, provider core.Provider, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return provider.Responses(ctx, req)
}

func callEmbeddings(ctx context.Context, provider core.Provider, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return provider.Embeddings(ctx, req)
}

// ChatCompletion routes the request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(route resolvedRoute) *core.ChatRequest {
			return r.plannedChatRequest(ctx, req, route)
		},
		callChatCompletion,
	)
}

// StreamChatCompletion routes the streaming request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return routeModelStream(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(route resolvedRoute) *core.ChatRequest {
			return r.plannedChatRequest(ctx, req, route)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ChatRequest) (io.ReadCloser, error) {
			return provider.StreamChatCompletion(ctx, forwardReq)
		},
	)
}

// Responses routes the Responses API request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(route resolvedRoute) *core.ResponsesRequest {
			return r.plannedResponsesRequest(req, route)
		},
		callResponses,
	)
}

// StreamResponses routes the streaming Responses API request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return routeModelStream(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(route resolvedRoute) *core.ResponsesRequest {
			return r.plannedResponsesRequest(req, route)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ResponsesRequest) (io.ReadCloser, error) {
			return provider.StreamResponses(ctx, forwardReq)
		},
	)
}

// Embeddings routes the embeddings request to the appropriate provider.
func (r *Router) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	resp, err := routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(route resolvedRoute) *core.EmbeddingRequest {
			return forwardEmbeddingRequest(req, route.selector)
		},
		callEmbeddings,
	)
	if err != nil {
		return nil, err
	}
	// Some OpenAI-compatible servers ignore encoding_format and always return
	// float arrays; re-encode to the format the client asked for so SDKs that
	// default to base64 (OpenAI, LangChain) don't mis-decode the response.
	core.NormalizeEmbeddingEncoding(resp, req.EncodingFormat)
	return resp, nil
}
