package gemini

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// googleCompatRejectedFields are Chat Completions members Google's
// OpenAI-compatible endpoint answers HTTP 400 for, as "Unknown name" or
// "Penalty is not enabled for this model" (verified October 2026 against
// gemini-4-argon). None has an equivalent on that surface, so they are dropped
// rather than failing the request.
var googleCompatRejectedFields = []string{
	"presence_penalty", "frequency_penalty", "seed", "logprobs", "top_logprobs",
	"store", "metadata", "logit_bias", "top_k", "prediction", "prompt_cache_key",
	"safety_identifier", "verbosity", "web_search_options",
}

// adaptChatRequest rewrites a ChatRequest for Gemini's OpenAI-compatible
// endpoint: members it rejects are dropped, unsigned Gemini 3+ tool calls get
// the validator placeholder, and the reasoning effort becomes the top-level
// "reasoning_effort" string the endpoint reads (not the nested
// "reasoning": {"effort": "..."} object), clamped to a level the model has.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	adapted := *req
	adapted.ExtraFields = req.ExtraFields.Without(googleCompatRejectedFields...)
	adapted.Messages = withCompatThoughtSignatures(req.Model, req.Messages)
	// Google's endpoint rejects allowed_tools ("Request contains an invalid
	// argument"), so the subset is applied to the tool list instead.
	tools, choice, err := core.NarrowToAllowedTools(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	adapted.Tools, adapted.ToolChoice = tools, choice
	effort := nativeReasoningEffort(req)
	if effort == "" {
		return &adapted, nil
	}
	return providers.AdaptReasoningEffortRequest(&adapted, compatReasoningEffort(req.Model, effort))
}

// compatReasoningEffort clamps reasoning_effort the way the native path does.
// Google's OpenAI-compatible endpoint forwards the value as a thinking level,
// so "minimal" is HTTP 400 on the models without that level (Gemini 3 Pro,
// 3.7 and 3.8 Flash, Gemini 4) and "none" cannot turn thinking off on any
// Gemini 3 or 4 model. Gemini 2.5 keeps the value: there it is a budget and
// "none" disables thinking.
func compatReasoningEffort(model, effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	if normalized != "none" && normalized != "minimal" {
		return effort
	}
	if major, _, ok := geminiGeneration(model); !ok || major < 3 {
		return effort
	}
	if supportsMinimalThinking(model) {
		return "minimal"
	}
	return "low"
}

// withCompatThoughtSignatures gives the unsigned assistant function calls of a
// Gemini 3+ history the validator-skipping placeholder, as the native path
// does. Google's OpenAI-compatible endpoint enforces the same rule (HTTP 400
// "Function call is missing a thought_signature") and accepts the placeholder
// under extra_content.google.thought_signature. Messages are copied only when
// a turn needs it.
func withCompatThoughtSignatures(model string, messages []core.Message) []core.Message {
	if !requiresThoughtSignature(model) {
		return messages
	}
	var out []core.Message
	for i, msg := range messages {
		if strings.TrimSpace(msg.Role) != "assistant" || len(msg.ToolCalls) == 0 || hasSignedToolCall(msg.ToolCalls) {
			continue
		}
		if out == nil {
			out = slices.Clone(messages)
		}
		calls := slices.Clone(msg.ToolCalls)
		for j := range calls {
			calls[j].ExtraFields = withPlaceholderSignature(calls[j].ExtraFields)
		}
		out[i].ToolCalls = calls
	}
	if out == nil {
		return messages
	}
	return out
}

func hasSignedToolCall(calls []core.ToolCall) bool {
	for _, call := range calls {
		if toolCallThoughtSignature(call) != "" {
			return true
		}
	}
	return false
}

func withPlaceholderSignature(fields core.UnknownJSONFields) core.UnknownJSONFields {
	encoded, err := json.Marshal(googleExtraContent{ThoughtSignature: skipThoughtSignatureValidator})
	if err != nil {
		return fields
	}
	signed, err := fields.WithExtraContent(core.ExtraContentVendorGoogle, encoded)
	if err != nil {
		return fields
	}
	return signed
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

// prepareNativeRequest applies the backend-specific members a translated
// generateContent request gets before it is sent: the AI Studio-only store
// flag and the cached-content prefix.
func (p *Provider) prepareNativeRequest(ctx context.Context, req *core.ChatRequest, body *geminiGenerateContentRequest) error {
	if p.backend == geminiBackendAIStudio {
		store, err := chatStore(req)
		if err != nil {
			return err
		}
		body.Store = store
	}
	p.prepareCachedContent(ctx, req, body)
	return nil
}

func (p *Provider) nativeChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	body, err := convertChatRequestToGemini(req)
	if err != nil {
		return nil, err
	}
	if err := p.prepareNativeRequest(ctx, req, body); err != nil {
		return nil, err
	}
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

	// Gemini's OpenAI-compatible endpoint returns OpenAI-format SSE; only its
	// tool-call chunks need repair on the way through.
	return newCompatToolCallStream(stream), nil
}

func (p *Provider) nativeStreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	streamReq := req.WithStreaming()
	body, err := convertChatRequestToGemini(streamReq)
	if err != nil {
		return nil, err
	}
	if err := p.prepareNativeRequest(ctx, req, body); err != nil {
		return nil, err
	}
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
	return providers.ResponsesViaChat(ctx, p, req, p.responseProviderName())
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
