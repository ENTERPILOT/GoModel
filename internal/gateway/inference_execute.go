package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// ExecuteChatCompletion executes a non-streaming chat request and records usage on success.
func (o *InferenceOrchestrator) ExecuteChatCompletion(ctx context.Context, workflow *core.Workflow, req *core.ChatRequest, requestID, endpoint string) (*ChatCompletionResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, err
	}
	return executeTranslatedResult(o, ctx, workflow, req, requestID, endpoint, chatExecutionSpec)
}

// DispatchChatCompletion executes a non-streaming chat request without usage side effects.
func (o *InferenceOrchestrator) DispatchChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, string, string, string, bool, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, "", "", "", false, err
	}
	return o.executeChatCompletion(ctx, workflow, req)
}

// StreamChatCompletion opens a chat SSE stream. Stream usage is recorded by the caller's stream observer.
func (o *InferenceOrchestrator) StreamChatCompletion(ctx context.Context, workflow *core.Workflow, req *core.ChatRequest) (*StreamResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, err
	}
	streamReq, providerType, providerName, usageModel := o.ResolveChatRoute(workflow, req)
	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, usedFallback, err := o.streamChatCompletion(ctx, workflow, streamReq, providerType, providerName, usageModel)
	if err != nil {
		return nil, err
	}
	return &StreamResult{
		Stream: stream,
		Meta: ExecutionMeta{
			ProviderType:  resolvedProviderType,
			ProviderName:  resolvedProviderName,
			Model:         resolvedUsageModel,
			FailoverModel: failoverModel,
			UsedFallback:  usedFallback,
		},
	}, nil
}

// ExecuteResponses executes a non-streaming Responses API request and records usage on success.
func (o *InferenceOrchestrator) ExecuteResponses(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest, requestID, endpoint string) (*ResponsesResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, err
	}
	return executeTranslatedResult(o, ctx, workflow, req, requestID, endpoint, responsesExecutionSpec)
}

// DispatchResponses executes a non-streaming Responses request without usage side effects.
func (o *InferenceOrchestrator) DispatchResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, string, string, string, bool, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, "", "", "", false, err
	}
	return o.executeResponses(ctx, workflow, req)
}

// StreamResponses opens a Responses API SSE stream. Stream usage is recorded by the caller's stream observer.
func (o *InferenceOrchestrator) StreamResponses(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest) (*StreamResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, err
	}
	providerType, providerName, usageModel := o.routeMetadata(workflow, req.Model)
	if (workflow == nil || workflow.UsageEnabled()) && o.ShouldEnforceReturningUsageData() {
		ctx = core.WithEnforceReturningUsageData(ctx, true)
	}
	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, usedFallback, err := o.streamResponses(ctx, workflow, req, providerType, providerName, usageModel)
	if err != nil {
		return nil, err
	}
	return &StreamResult{
		Stream: stream,
		Meta: ExecutionMeta{
			ProviderType:  resolvedProviderType,
			ProviderName:  resolvedProviderName,
			Model:         resolvedUsageModel,
			FailoverModel: failoverModel,
			UsedFallback:  usedFallback,
		},
	}, nil
}

// ExecuteEmbeddings executes an embeddings request and records usage on success.
func (o *InferenceOrchestrator) ExecuteEmbeddings(ctx context.Context, workflow *core.Workflow, req *core.EmbeddingRequest, requestID, endpoint string) (*EmbeddingResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "embeddings request is required"); err != nil {
		return nil, err
	}
	resp, providerType, providerName, err := o.executeEmbeddings(ctx, workflow, req)
	if err != nil {
		return nil, err
	}
	o.logUsage(ctx, workflow, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, endpoint, pricing)
	})
	return &EmbeddingResult{
		Response: resp,
		Meta: ExecutionMeta{
			ProviderType: providerType,
			ProviderName: providerName,
			Model:        resp.Model,
		},
	}, nil
}

// DispatchEmbeddings executes an embeddings request without usage side effects.
func (o *InferenceOrchestrator) DispatchEmbeddings(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.EmbeddingRequest,
) (*core.EmbeddingResponse, string, string, error) {
	if err := o.validateProviderAndRequest(req != nil, "embeddings request is required"); err != nil {
		return nil, "", "", err
	}
	return o.executeEmbeddings(ctx, workflow, req)
}

// ResolveChatRoute returns the provider route and the request to send for chat streams.
func (o *InferenceOrchestrator) ResolveChatRoute(workflow *core.Workflow, req *core.ChatRequest) (*core.ChatRequest, string, string, string) {
	providerType, providerName, usageModel := o.routeMetadata(workflow, "")
	if req != nil {
		usageModel = ResolvedModelFromWorkflow(workflow, req.Model)
	}
	if req == nil || !req.Stream || (workflow != nil && !workflow.UsageEnabled()) || !o.ShouldEnforceReturningUsageData() {
		return req, providerType, providerName, usageModel
	}

	streamReq := CloneChatRequestForStreamUsage(req)
	if streamReq.StreamOptions == nil {
		streamReq.StreamOptions = &core.StreamOptions{}
	}
	streamReq.StreamOptions.IncludeUsage = true
	return streamReq, providerType, providerName, usageModel
}

func (o *InferenceOrchestrator) routeMetadata(workflow *core.Workflow, fallbackModel string) (string, string, string) {
	providerType := ProviderTypeFromWorkflow(workflow)
	providerName := ProviderNameFromWorkflow(workflow)
	model := ResolvedModelFromWorkflow(workflow, fallbackModel)
	return providerType, providerName, model
}

// CanFastPathStreamingChatPassthrough reports whether a streaming chat request can bypass translation.
func (o *InferenceOrchestrator) CanFastPathStreamingChatPassthrough(workflow *core.Workflow, req *core.ChatRequest) bool {
	if req == nil || !req.Stream {
		return false
	}
	if o.translatedRequestPatcher != nil || o.ShouldEnforceReturningUsageData() {
		return false
	}
	if workflow == nil || workflow.Resolution == nil {
		return false
	}

	providerType := strings.ToLower(strings.TrimSpace(workflow.ProviderType))
	switch providerType {
	case "openai", "azure", "openrouter":
	default:
		return false
	}

	if translatedStreamingSelectorRewriteRequired(workflow.Resolution) {
		return false
	}
	if translatedStreamingChatBodyRewriteRequired(req) {
		return false
	}

	return true
}

func translatedStreamingSelectorRewriteRequired(resolution *core.RequestModelResolution) bool {
	if resolution == nil {
		return true
	}

	requestedModel := strings.TrimSpace(resolution.Requested.Model)
	requestedProvider := strings.TrimSpace(resolution.Requested.ProviderHint)
	resolvedModel := strings.TrimSpace(resolution.ResolvedSelector.Model)
	resolvedProvider := strings.TrimSpace(resolution.ResolvedSelector.Provider)

	return requestedModel != resolvedModel || requestedProvider != resolvedProvider
}

func translatedStreamingChatBodyRewriteRequired(req *core.ChatRequest) bool {
	if req == nil {
		return true
	}
	if strings.TrimSpace(req.Provider) != "" {
		return true
	}

	model := strings.ToLower(strings.TrimSpace(req.Model))
	oSeries := len(model) >= 2 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9'
	return oSeries && (req.MaxTokens != nil || req.Temperature != nil)
}

func (o *InferenceOrchestrator) executeChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, string, string, string, bool, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, "", "", "", false, err
	}
	return executeTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, CloneChatRequestForSelector, o.chatCompletionProviderCall, chatResponseProvider)
}

func (o *InferenceOrchestrator) streamChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, string, string, string, string, bool, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, "", "", "", "", false, err
	}
	return streamTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, providerType, providerName, usageModel, CloneChatRequestForSelector, o.streamChatCompletionProviderCall)
}

func (o *InferenceOrchestrator) executeResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, string, string, string, bool, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, "", "", "", false, err
	}
	return executeTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, CloneResponsesRequestForSelector, o.responsesProviderCall, responsesResponseProvider)
}

func (o *InferenceOrchestrator) streamResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, string, string, string, string, bool, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, "", "", "", "", false, err
	}
	return streamTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, providerType, providerName, usageModel, CloneResponsesRequestForSelector, o.streamResponsesProviderCall)
}

type translatedExecutionSpec[Req any, Resp any, Result any] struct {
	execute func(*InferenceOrchestrator, context.Context, *core.Workflow, Req) (Resp, string, string, string, bool, error)
	model   func(Resp) string
	usage   func(Resp, string, string, string, *core.ModelPricing) *usage.UsageEntry
	build   func(Resp, ExecutionMeta) Result
}

var chatExecutionSpec = translatedExecutionSpec[*core.ChatRequest, *core.ChatResponse, *ChatCompletionResult]{
	execute: executeChatCompletionRequest,
	model:   chatResponseModel,
	usage: func(resp *core.ChatResponse, requestID, providerType, endpoint string, pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromChatResponse(resp, requestID, providerType, endpoint, pricing)
	},
	build: func(resp *core.ChatResponse, meta ExecutionMeta) *ChatCompletionResult {
		return &ChatCompletionResult{Response: resp, Meta: meta}
	},
}

var responsesExecutionSpec = translatedExecutionSpec[*core.ResponsesRequest, *core.ResponsesResponse, *ResponsesResult]{
	execute: executeResponsesRequest,
	model:   responsesResponseModel,
	usage: func(resp *core.ResponsesResponse, requestID, providerType, endpoint string, pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromResponsesResponse(resp, requestID, providerType, endpoint, pricing)
	},
	build: func(resp *core.ResponsesResponse, meta ExecutionMeta) *ResponsesResult {
		return &ResponsesResult{Response: resp, Meta: meta}
	},
}

func executeTranslatedResult[Req any, Resp any, Result any](
	o *InferenceOrchestrator,
	ctx context.Context,
	workflow *core.Workflow,
	req Req,
	requestID, endpoint string,
	spec translatedExecutionSpec[Req, Resp, Result],
) (Result, error) {
	resp, meta, err := executeWithUsage(o, ctx, workflow,
		func() (Resp, string, string, string, bool, error) {
			return spec.execute(o, ctx, workflow, req)
		},
		spec.model,
		func(resp Resp, providerType string, pricing *core.ModelPricing) *usage.UsageEntry {
			return spec.usage(resp, requestID, providerType, endpoint, pricing)
		},
	)
	if err != nil {
		var zero Result
		return zero, err
	}
	return spec.build(resp, meta), nil
}

func executeWithUsage[Resp any](
	o *InferenceOrchestrator,
	ctx context.Context,
	workflow *core.Workflow,
	execute func() (Resp, string, string, string, bool, error),
	modelFromResponse func(Resp) string,
	entry func(Resp, string, *core.ModelPricing) *usage.UsageEntry,
) (Resp, ExecutionMeta, error) {
	resp, providerType, providerName, failoverModel, usedFallback, err := execute()
	if err != nil {
		var zero Resp
		return zero, ExecutionMeta{}, err
	}
	model := modelFromResponse(resp)
	o.logUsage(ctx, workflow, model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return entry(resp, providerType, pricing)
	})
	return resp, ExecutionMeta{
		ProviderType:  providerType,
		ProviderName:  providerName,
		Model:         model,
		FailoverModel: failoverModel,
		UsedFallback:  usedFallback,
	}, nil
}

func executeTranslatedProviderRequest[Req any, Resp any](
	o *InferenceOrchestrator,
	ctx context.Context,
	workflow *core.Workflow,
	req Req,
	model, provider string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req) (Resp, error),
	responseProvider func(Resp) string,
) (Resp, string, string, string, bool, error) {
	return executeTranslatedWithFallback(ctx, o, workflow, req, model, provider, cloneForSelector,
		func(ctx context.Context, req Req) (Resp, string, error) {
			resp, err := call(ctx, req)
			if err != nil {
				var zero Resp
				return zero, "", err
			}
			return resp, responseProvider(resp), nil
		},
	)
}

func streamTranslatedProviderRequest[Req any](
	o *InferenceOrchestrator,
	ctx context.Context,
	workflow *core.Workflow,
	req Req,
	model, provider string,
	providerType, providerName, usageModel string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req) (io.ReadCloser, error),
) (io.ReadCloser, string, string, string, string, bool, error) {
	stream, err := call(ctx, req)
	if err == nil && stream != nil {
		return stream, providerType, providerName, usageModel, "", false, nil
	}
	if err == nil {
		err = emptyProviderStreamError(providerType)
	}

	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, err := tryFallbackStream(ctx, o, workflow, model, provider, err,
		func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error) {
			stream, err := call(ctx, cloneForSelector(req, selector))
			if err != nil {
				return nil, "", "", err
			}
			if stream == nil {
				return nil, "", "", emptyProviderStreamError(providerType)
			}
			return stream, providerType, selector.Model, nil
		},
	)
	if err != nil {
		return nil, "", "", "", "", false, err
	}
	return stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, true, nil
}

func (o *InferenceOrchestrator) validateProviderAndRequest(requestPresent bool, requiredMessage string) *core.GatewayError {
	if o == nil || o.provider == nil {
		return core.NewInvalidRequestError("provider is not configured", nil)
	}
	if !requestPresent {
		return core.NewInvalidRequestError(requiredMessage, nil)
	}
	return nil
}

func (o *InferenceOrchestrator) chatCompletionProviderCall(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	resp, err := o.provider.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, emptyProviderResponseError("")
	}
	return resp, nil
}

func (o *InferenceOrchestrator) responsesProviderCall(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	resp, err := o.provider.Responses(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, emptyProviderResponseError("")
	}
	return resp, nil
}

func (o *InferenceOrchestrator) streamChatCompletionProviderCall(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return o.provider.StreamChatCompletion(ctx, req)
}

func (o *InferenceOrchestrator) streamResponsesProviderCall(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return o.provider.StreamResponses(ctx, req)
}

func emptyProviderResponseError(providerType string) *core.GatewayError {
	return core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil)
}

func emptyProviderStreamError(providerType string) *core.GatewayError {
	return core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty stream", nil)
}

func executeChatCompletionRequest(
	o *InferenceOrchestrator,
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, string, string, string, bool, error) {
	return o.executeChatCompletion(ctx, workflow, req)
}

func executeResponsesRequest(
	o *InferenceOrchestrator,
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, string, string, string, bool, error) {
	return o.executeResponses(ctx, workflow, req)
}

func chatResponseModel(resp *core.ChatResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Model
}

func chatResponseProvider(resp *core.ChatResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Provider
}

func responsesResponseModel(resp *core.ResponsesResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Model
}

func responsesResponseProvider(resp *core.ResponsesResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Provider
}

func (o *InferenceOrchestrator) executeEmbeddings(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.EmbeddingRequest,
) (*core.EmbeddingResponse, string, string, error) {
	if err := o.validateProviderAndRequest(req != nil, "embeddings request is required"); err != nil {
		return nil, "", "", err
	}
	providerType := ProviderTypeFromWorkflow(workflow)
	providerName := ProviderNameFromWorkflow(workflow)
	resp, err := o.provider.Embeddings(ctx, req)
	if err == nil {
		if resp == nil {
			return nil, "", "", emptyProviderResponseError(providerType)
		}
		return resp, ResponseProviderType(providerType, resp.Provider), providerName, nil
	}

	return o.tryFallbackEmbeddings(ctx, workflow, req, err)
}

func (o *InferenceOrchestrator) tryFallbackEmbeddings(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.EmbeddingRequest,
	primaryErr error,
) (*core.EmbeddingResponse, string, string, error) {
	// Embeddings fallback is intentionally disabled until the shared model
	// contract can prove vector-size compatibility for alternates.
	return nil, "", "", primaryErr
}
