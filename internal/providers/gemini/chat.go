package gemini

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// adaptChatRequest rewrites a ChatRequest for Gemini's OpenAI-compatible endpoint.
// Gemini uses "reasoning_effort" as a top-level string (e.g. "low", "medium", "high"),
// not the nested "reasoning": {"effort": "..."} format.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req.Reasoning == nil || req.Reasoning.Effort == "" {
		return req, nil
	}
	return providers.AdaptReasoningEffortRequest(req, req.Reasoning.Effort)
}

func (p *Provider) openAICompatibleChatBody(req *core.ChatRequest) (any, error) {
	if p.backend == geminiBackendVertex {
		if model := vertexOpenAIModelID(req.Model); strings.TrimSpace(model) != "" {
			rewritten := *req
			rewritten.Model = model
			req = &rewritten
		}
	}
	return adaptChatRequest(req)
}

func (p *Provider) openAICompatibleEmbeddingBody(req *core.EmbeddingRequest) (any, error) {
	if p.backend != geminiBackendVertex {
		return req, nil
	}
	model := vertexOpenAIModelID(req.Model)
	if strings.TrimSpace(model) == "" {
		return req, nil
	}
	rewritten := *req
	rewritten.Model = model
	return &rewritten, nil
}

// ChatCompletion sends a chat completion request to Gemini
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if p.useNativeAPI {
		return p.nativeChatCompletion(ctx, req)
	}
	body, err := p.openAICompatibleChatBody(req)
	if err != nil {
		return nil, err
	}
	var resp core.ChatResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/chat/completions",
		Operation: llmclient.OperationChat,
		Model:     req.Model,
		Body:      body,
	}, &resp)
	if err != nil {
		return nil, err
	}
	core.EnsureModel(&resp.Model, req.Model)
	if resp.Provider == "" {
		resp.Provider = p.responseProviderName()
	}
	return &resp, nil
}

func (p *Provider) nativeChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	body, err := convertChatRequestToGemini(req)
	if err != nil {
		return nil, err
	}
	p.prepareCachedContent(ctx, req, body)
	var geminiResp geminiGenerateContentResponse
	err = p.nativeClient.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  nativeGenerateEndpoint(req.Model),
		Operation: llmclient.OperationGenerateContent,
		Model:     req.Model,
		Body:      body,
	}, &geminiResp)
	if err != nil {
		return nil, err
	}
	return nativeChatResponse(req, &geminiResp, p.responseProviderName())
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if p.useNativeAPI {
		return p.nativeStreamChatCompletion(ctx, req)
	}
	streamReq := req.WithStreaming()
	body, err := p.openAICompatibleChatBody(streamReq)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/chat/completions",
		Operation: llmclient.OperationChat,
		Model:     req.Model,
		Body:      body,
	})
	if err != nil {
		return nil, err
	}

	// Gemini's OpenAI-compatible endpoint returns OpenAI-format SSE; normalize
	// it like every other OpenAI-compatible upstream.
	return providers.EnsureChatCompletionSSE(p.responseProviderName(), stream)
}

func (p *Provider) nativeStreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	streamReq := req.WithStreaming()
	body, err := convertChatRequestToGemini(streamReq)
	if err != nil {
		return nil, err
	}
	p.prepareCachedContent(ctx, req, body)
	stream, err := p.nativeClient.DoStream(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  nativeStreamEndpoint(req.Model),
		Operation: llmclient.OperationGenerateContent,
		Model:     req.Model,
		Body:      body,
	})
	if err != nil {
		return nil, err
	}
	includeUsage := streamReq.StreamOptions != nil && streamReq.StreamOptions.IncludeUsage
	return newGeminiNativeStream(stream, req.Model, includeUsage, p.responseProviderName()), nil
}

// Responses sends a Responses API request to Gemini (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.ResponsesViaChat(ctx, p, req)
}

// Embeddings sends an embeddings request to Gemini: the native
// batchEmbedContents API in native mode, or the OpenAI-compatible endpoint
// otherwise. The Vertex backend always uses the OpenAI-compatible surface —
// Vertex has no batchEmbedContents; its native prediction path is served by
// the dedicated vertex provider.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("embedding request is required", nil)
	}
	if p.useNativeAPI && p.backend == geminiBackendAIStudio {
		return p.nativeEmbeddings(ctx, req)
	}
	body, err := p.openAICompatibleEmbeddingBody(req)
	if err != nil {
		return nil, err
	}
	var resp core.EmbeddingResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/embeddings",
		Operation: llmclient.OperationEmbeddings,
		Model:     req.Model,
		Body:      body,
	}, &resp)
	if err != nil {
		return nil, err
	}
	core.EnsureModel(&resp.Model, req.Model)
	if resp.Provider == "" {
		resp.Provider = p.responseProviderName()
	}
	return &resp, nil
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.StreamResponsesViaChat(ctx, p, req, p.responseProviderName())
}
