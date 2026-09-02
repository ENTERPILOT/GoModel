package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// ExecuteChatCompletion executes a non-streaming chat request and records usage on success.
func (o *InferenceOrchestrator) ExecuteChatCompletion(ctx context.Context, workflow *core.Workflow, req *core.ChatRequest, requestID, endpoint string) (*ChatCompletionResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, err
	}
	return executeResultWithSlowdown(ctx, workflow, func() (*ChatCompletionResult, error) {
		return executeTranslatedResult(o, ctx, workflow, req, requestID, endpoint, chatExecutionSpec)
	})
}

// DispatchChatCompletion executes a non-streaming chat request without usage side effects.
func (o *InferenceOrchestrator) DispatchChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, ExecutionMeta, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, ExecutionMeta{}, err
	}
	return dispatchTranslatedWithSlowdown(ctx, workflow, func() (*core.ChatResponse, ExecutionMeta, error) {
		return o.executeChatCompletion(ctx, workflow, req)
	})
}

// StreamChatCompletion opens a chat SSE stream. Stream usage is recorded by the caller's stream observer.
func (o *InferenceOrchestrator) StreamChatCompletion(ctx context.Context, workflow *core.Workflow, req *core.ChatRequest) (*StreamResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, err
	}
	started := time.Now()
	streamReq, providerType, providerName, usageModel := o.ResolveChatRoute(workflow, req)
	stream, meta, err := o.streamChatCompletion(ctx, workflow, streamReq, providerType, providerName, usageModel)
	if err != nil {
		return nil, err
	}
	return &StreamResult{
		Stream:           stream,
		slowdownFactor:   workflowSlowdown(workflow),
		inferenceStarted: started,
		Meta:             meta,
	}, nil
}

// ExecuteResponses executes a non-streaming Responses API request and records usage on success.
func (o *InferenceOrchestrator) ExecuteResponses(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest, requestID, endpoint string) (*ResponsesResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, err
	}
	return executeResultWithSlowdown(ctx, workflow, func() (*ResponsesResult, error) {
		return executeTranslatedResult(o, ctx, workflow, req, requestID, endpoint, responsesExecutionSpec)
	})
}

// DispatchResponses executes a non-streaming Responses request without usage side effects.
func (o *InferenceOrchestrator) DispatchResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, ExecutionMeta, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, ExecutionMeta{}, err
	}
	return dispatchTranslatedWithSlowdown(ctx, workflow, func() (*core.ResponsesResponse, ExecutionMeta, error) {
		return o.executeResponses(ctx, workflow, req)
	})
}

// StreamResponses opens a Responses API SSE stream. Stream usage is recorded by the caller's stream observer.
func (o *InferenceOrchestrator) StreamResponses(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest) (*StreamResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, err
	}
	started := time.Now()
	providerType, providerName, usageModel := o.routeMetadata(workflow, req.Model)
	if (workflow == nil || workflow.UsageEnabled()) && o.ShouldEnforceReturningUsageData() {
		ctx = core.WithEnforceReturningUsageData(ctx, true)
	}
	stream, meta, err := o.streamResponses(ctx, workflow, req, providerType, providerName, usageModel)
	if err != nil {
		return nil, err
	}
	return &StreamResult{
		Stream:           stream,
		slowdownFactor:   workflowSlowdown(workflow),
		inferenceStarted: started,
		Meta:             meta,
	}, nil
}

// ExecuteEmbeddings executes an embeddings request and records usage on success.
func (o *InferenceOrchestrator) ExecuteEmbeddings(ctx context.Context, workflow *core.Workflow, req *core.EmbeddingRequest, requestID, endpoint string) (*EmbeddingResult, error) {
	if err := o.validateProviderAndRequest(req != nil, "embeddings request is required"); err != nil {
		return nil, err
	}
	started := time.Now()
	resp, providerType, providerName, err := o.executeEmbeddings(ctx, workflow, req)
	if err != nil {
		return nil, err
	}
	pricingModel := usagePricingModel(workflow, req.Model, "", resp.Model)
	o.logUsage(ctx, workflow, pricingModel, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, endpoint, pricing)
	})
	if err := waitForInferenceSlowdown(ctx, workflow, time.Since(started)); err != nil {
		return nil, err
	}
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
	started := time.Now()
	resp, providerType, providerName, err := o.executeEmbeddings(ctx, workflow, req)
	if err != nil {
		return nil, "", "", err
	}
	if err := waitForInferenceSlowdown(ctx, workflow, time.Since(started)); err != nil {
		return nil, "", "", err
	}
	return resp, providerType, providerName, nil
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

func (o *InferenceOrchestrator) routeMetadata(workflow *core.Workflow, failoverModel string) (string, string, string) {
	providerType := ProviderTypeFromWorkflow(workflow)
	providerName := ProviderNameFromWorkflow(workflow)
	model := ResolvedModelFromWorkflow(workflow, failoverModel)
	return providerType, providerName, model
}

// chatCachePlanner is implemented by routers that run a prompt-cache planner
// after routing. The passthrough fast path bypasses routing, so it must decline
// any request the planner would have annotated.
type chatCachePlanner interface {
	PromptCachePlanApplies(providerType string, selector core.ModelSelector, req *core.ChatRequest) bool
}

// CanFastPathChatPassthrough reports whether a chat request, streaming or not,
// can bypass translation and be proxied to the provider byte for byte. It
// declines whenever the translated path would change the outbound request:
// slowdown, request patching, selector or body rewrites, prompt-cache planning,
// and enforced usage data. Enforcement only rewrites streaming requests (it
// injects stream_options.include_usage); a non-streaming JSON response always
// carries usage, so it does not block the fast path.
func (o *InferenceOrchestrator) CanFastPathChatPassthrough(workflow *core.Workflow, req *core.ChatRequest) bool {
	if req == nil {
		return false
	}
	if workflowSlowdown(workflow) > 0 {
		return false
	}
	if o.translatedRequestPatcher != nil {
		return false
	}
	if req.Stream && o.ShouldEnforceReturningUsageData() {
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

	if translatedSelectorRewriteRequired(workflow.Resolution) {
		return false
	}
	if translatedChatBodyRewriteRequired(req) {
		return false
	}
	if planner, ok := o.provider.(chatCachePlanner); ok &&
		planner.PromptCachePlanApplies(providerType, workflow.Resolution.ResolvedSelector, req) {
		return false
	}

	return true
}

// translatedSelectorRewriteRequired reports whether the model string the client
// sent differs from the one the provider must receive. Only the model travels
// upstream: translation strips the "provider" field, and a request carrying one
// is already declined by translatedChatBodyRewriteRequired. The resolved
// provider name is therefore not compared; a bare "gpt-4o" that resolves to the
// configured "openai" instance still forwards "gpt-4o" verbatim, while an alias
// or a provider-qualified "openai/gpt-4o" does not.
func translatedSelectorRewriteRequired(resolution *core.RequestModelResolution) bool {
	if resolution == nil || resolution.Requested.ExplicitProvider {
		return true
	}
	return strings.TrimSpace(resolution.Requested.Model) != strings.TrimSpace(resolution.ResolvedSelector.Model)
}

// translatedChatBodyRewriteRequired reports whether translation would change a
// field other than the model selector. req.Provider is not consulted: by the
// time dispatch runs, preparation has already written the resolved provider
// into it, so it no longer says whether the client sent one. A client-supplied
// provider is caught by Requested.ExplicitProvider in
// translatedSelectorRewriteRequired.
func translatedChatBodyRewriteRequired(req *core.ChatRequest) bool {
	if req == nil {
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
) (*core.ChatResponse, ExecutionMeta, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, ExecutionMeta{}, err
	}
	return executeTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, CloneChatRequestForSelector, o.chatCompletionProviderCall, chatResponseProvider)
}

func (o *InferenceOrchestrator) streamChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, ExecutionMeta, error) {
	if err := o.validateProviderAndRequest(req != nil, "chat request is required"); err != nil {
		return nil, ExecutionMeta{}, err
	}
	return streamTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, providerType, providerName, usageModel, CloneChatRequestForSelector, o.streamChatCompletionProviderCall)
}

func (o *InferenceOrchestrator) executeResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, ExecutionMeta, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, ExecutionMeta{}, err
	}
	return executeTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, CloneResponsesRequestForSelector, o.responsesProviderCall, responsesResponseProvider)
}

func (o *InferenceOrchestrator) streamResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, ExecutionMeta, error) {
	if err := o.validateProviderAndRequest(req != nil, "responses request is required"); err != nil {
		return nil, ExecutionMeta{}, err
	}
	return streamTranslatedProviderRequest(o, ctx, workflow, req, req.Model, req.Provider, providerType, providerName, usageModel, CloneResponsesRequestForSelector, o.streamResponsesProviderCall)
}

type translatedExecutionSpec[Req any, Resp any, Result any] struct {
	execute func(*InferenceOrchestrator, context.Context, *core.Workflow, Req) (Resp, ExecutionMeta, error)
	model   func(Resp) string
	request func(Req) string
	usage   func(Resp, string, string, string, *core.ModelPricing) *usage.UsageEntry
	build   func(Resp, ExecutionMeta) Result
}

var chatExecutionSpec = translatedExecutionSpec[*core.ChatRequest, *core.ChatResponse, *ChatCompletionResult]{
	execute: (*InferenceOrchestrator).executeChatCompletion,
	model:   chatResponseModel,
	request: chatRequestModel,
	usage: func(resp *core.ChatResponse, requestID, providerType, endpoint string, pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromChatResponse(resp, requestID, providerType, endpoint, pricing)
	},
	build: func(resp *core.ChatResponse, meta ExecutionMeta) *ChatCompletionResult {
		return &ChatCompletionResult{Response: resp, Meta: meta}
	},
}

var responsesExecutionSpec = translatedExecutionSpec[*core.ResponsesRequest, *core.ResponsesResponse, *ResponsesResult]{
	execute: (*InferenceOrchestrator).executeResponses,
	model:   responsesResponseModel,
	request: responsesRequestModel,
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
		func() (Resp, ExecutionMeta, error) {
			return spec.execute(o, ctx, workflow, req)
		},
		requestModel(req, spec.request),
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
	execute func() (Resp, ExecutionMeta, error),
	requestedModel string,
	modelFromResponse func(Resp) string,
	entry func(Resp, string, *core.ModelPricing) *usage.UsageEntry,
) (Resp, ExecutionMeta, error) {
	resp, meta, err := execute()
	if err != nil {
		var zero Resp
		return zero, ExecutionMeta{}, err
	}
	meta.Model = modelFromResponse(resp)
	pricingModel := usagePricingModel(workflow, requestedModel, meta.FailoverModel, meta.Model)
	o.logUsage(ctx, workflow, pricingModel, meta.ProviderType, meta.ProviderName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return entry(resp, meta.ProviderType, pricing)
	})
	return resp, meta, nil
}

func requestModel[Req any](req Req, model func(Req) string) string {
	if model == nil {
		return ""
	}
	return strings.TrimSpace(model(req))
}

func usagePricingModel(workflow *core.Workflow, requestedModel, failoverModel, responseModel string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	failoverModel = strings.TrimSpace(failoverModel)
	if failoverModel != "" {
		return failoverModel
	}
	if model := ResolvedModelFromWorkflow(workflow, requestedModel); model != "" {
		return model
	}
	return strings.TrimSpace(responseModel)
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
) (Resp, ExecutionMeta, error) {
	return executeTranslatedWithFailover(ctx, o, workflow, req, model, provider, cloneForSelector,
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
) (io.ReadCloser, ExecutionMeta, error) {
	started := time.Now()
	var stream io.ReadCloser
	// See executeTranslatedWithFailover: a rate-saturated primary route skips
	// the provider call and enters the failover sweep with its stored 429.
	err := core.PrimaryRouteSaturated(ctx)
	if err == nil {
		stream, err = call(ctx, req)
		if err == nil && stream != nil {
			recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, providerType, providerName, currentSelectorForWorkflow(workflow, model, provider), started, nil))
			return stream, ExecutionMeta{ProviderType: providerType, ProviderName: providerName, Model: usageModel}, nil
		}
		if err == nil {
			err = emptyProviderStreamError(providerType)
		}
	}
	recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, providerType, providerName, currentSelectorForWorkflow(workflow, model, provider), started, err))

	return tryFailoverStream(ctx, o, workflow, model, provider, err,
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
	return core.NewEmptyProviderResponseError(providerType)
}

func emptyProviderStreamError(providerType string) *core.GatewayError {
	return core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty stream", nil)
}

func chatResponseModel(resp *core.ChatResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Model
}

func chatRequestModel(req *core.ChatRequest) string {
	if req == nil {
		return ""
	}
	return req.Model
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

func responsesRequestModel(req *core.ResponsesRequest) string {
	if req == nil {
		return ""
	}
	return req.Model
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

	return o.tryFailoverEmbeddings(ctx, workflow, req, err)
}

func (o *InferenceOrchestrator) tryFailoverEmbeddings(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.EmbeddingRequest,
	primaryErr error,
) (*core.EmbeddingResponse, string, string, error) {
	// Embeddings failover is intentionally disabled until the shared model
	// contract can prove vector-size compatibility for alternates.
	return nil, "", "", primaryErr
}
