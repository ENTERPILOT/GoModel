package gateway

import (
	"context"
	"strings"

	"gomodel/internal/core"
)

// PrepareChatRequest resolves workflow/model policy and applies translated request patching.
func (o *InferenceOrchestrator) PrepareChatRequest(ctx context.Context, req *core.ChatRequest, meta RequestMeta) (*PreparedChatRequest, error) {
	return prepareTranslated(o, ctx, req, meta, chatPrepareSpec)
}

// PrepareResponsesRequest resolves workflow/model policy and applies translated request patching.
func (o *InferenceOrchestrator) PrepareResponsesRequest(ctx context.Context, req *core.ResponsesRequest, meta RequestMeta) (*PreparedResponsesRequest, error) {
	return prepareTranslated(o, ctx, req, meta, responsesPrepareSpec)
}

// PrepareEmbeddingRequest resolves workflow/model policy for an embeddings request.
func (o *InferenceOrchestrator) PrepareEmbeddingRequest(ctx context.Context, req *core.EmbeddingRequest, meta RequestMeta) (*PreparedEmbeddingRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("embeddings request is required", nil)
	}
	ctx = contextWithRequestID(ctx, meta.RequestID)
	workflow, err := o.ensureTranslatedRequestWorkflow(ctx, meta.Workflow, meta.RequestID, meta.Endpoint, &req.Model, &req.Provider)
	if err != nil {
		return nil, err
	}
	ctx = o.WithCacheRequestContext(ctx, workflow)
	return &PreparedEmbeddingRequest{Context: ctx, Request: req, Workflow: workflow}, nil
}

type translatedPrepareSpec[Req any, Prepared any] struct {
	requiredMessage string
	patchNilMessage string
	selector        func(Req) (*string, *string)
	patch           func(*InferenceOrchestrator) func(context.Context, Req) (Req, error)
	valid           func(Req) bool
	build           func(context.Context, Req, *core.Workflow) Prepared
}

var chatPrepareSpec = translatedPrepareSpec[*core.ChatRequest, *PreparedChatRequest]{
	requiredMessage: "chat request is required",
	patchNilMessage: "patched chat request is required",
	selector:        chatRequestSelector,
	patch:           chatRequestPatch,
	valid:           validChatRequest,
	build: func(ctx context.Context, req *core.ChatRequest, workflow *core.Workflow) *PreparedChatRequest {
		return &PreparedChatRequest{Context: ctx, Request: req, Workflow: workflow}
	},
}

var responsesPrepareSpec = translatedPrepareSpec[*core.ResponsesRequest, *PreparedResponsesRequest]{
	requiredMessage: "responses request is required",
	patchNilMessage: "patched responses request is required",
	selector:        responsesRequestSelector,
	patch:           responsesRequestPatch,
	valid:           validResponsesRequest,
	build: func(ctx context.Context, req *core.ResponsesRequest, workflow *core.Workflow) *PreparedResponsesRequest {
		return &PreparedResponsesRequest{Context: ctx, Request: req, Workflow: workflow}
	},
}

func prepareTranslated[Req any, Prepared any](
	o *InferenceOrchestrator,
	ctx context.Context,
	req Req,
	meta RequestMeta,
	spec translatedPrepareSpec[Req, Prepared],
) (Prepared, error) {
	var zero Prepared
	if !spec.valid(req) {
		return zero, core.NewInvalidRequestError(spec.requiredMessage, nil)
	}
	model, provider := spec.selector(req)
	ctx, req, workflow, err := prepareTranslatedRequest(o, ctx, req, meta, model, provider, spec.patch(o), spec.valid, spec.patchNilMessage)
	if err != nil {
		return zero, err
	}
	return spec.build(ctx, req, workflow), nil
}

func prepareTranslatedRequest[Req any](
	o *InferenceOrchestrator,
	ctx context.Context,
	req Req,
	meta RequestMeta,
	model,
	provider *string,
	patch func(context.Context, Req) (Req, error),
	valid func(Req) bool,
	patchNilMessage string,
) (context.Context, Req, *core.Workflow, error) {
	ctx = contextWithRequestID(ctx, meta.RequestID)
	workflow, err := o.ensureTranslatedRequestWorkflow(ctx, meta.Workflow, meta.RequestID, meta.Endpoint, model, provider)
	if err != nil {
		var zero Req
		return ctx, zero, nil, err
	}
	ctx = core.WithWorkflow(ctx, workflow)
	if patch != nil {
		req, err = patch(ctx, req)
		if err != nil {
			var zero Req
			return ctx, zero, nil, err
		}
		if valid != nil && !valid(req) {
			var zero Req
			return ctx, zero, nil, core.NewInvalidRequestError(patchNilMessage, nil)
		}
	}
	ctx = o.WithCacheRequestContext(ctx, workflow)
	return ctx, req, workflow, nil
}

func validChatRequest(req *core.ChatRequest) bool {
	return req != nil
}

func validResponsesRequest(req *core.ResponsesRequest) bool {
	return req != nil
}

func chatRequestSelector(req *core.ChatRequest) (*string, *string) {
	return &req.Model, &req.Provider
}

func responsesRequestSelector(req *core.ResponsesRequest) (*string, *string) {
	return &req.Model, &req.Provider
}

func chatRequestPatch(o *InferenceOrchestrator) func(context.Context, *core.ChatRequest) (*core.ChatRequest, error) {
	if o.translatedRequestPatcher == nil {
		return nil
	}
	return o.translatedRequestPatcher.PatchChatRequest
}

func responsesRequestPatch(o *InferenceOrchestrator) func(context.Context, *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	if o.translatedRequestPatcher == nil {
		return nil
	}
	return o.translatedRequestPatcher.PatchResponsesRequest
}

func contextWithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || strings.TrimSpace(core.GetRequestID(ctx)) == requestID {
		return ctx
	}
	return core.WithRequestID(ctx, requestID)
}

func (o *InferenceOrchestrator) ensureTranslatedRequestWorkflow(
	ctx context.Context,
	current *core.Workflow,
	requestID string,
	endpoint core.EndpointDescriptor,
	model,
	providerHint *string,
) (*core.Workflow, error) {
	if model == nil || providerHint == nil {
		return nil, core.NewInvalidRequestError("model selector targets are required", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	workflow := currentTranslatedWorkflow(current, endpoint)
	var err error
	if workflow == nil {
		resolution, err := ResolveRequestModelWithAuthorizer(ctx, o.provider, o.modelResolver, o.modelAuthorizer, core.NewRequestedModelSelector(*model, *providerHint))
		if err != nil {
			return nil, err
		}
		workflow, err = TranslatedWorkflow(ctx, strings.TrimSpace(requestID), endpoint, resolution, o.workflowPolicyResolver)
		if err != nil {
			return nil, err
		}
		ApplyResolvedSelector(model, providerHint, resolution)
		return workflow, nil
	}

	resolution := workflow.Resolution
	if resolution != nil && o.modelAuthorizer != nil {
		if err := o.modelAuthorizer.ValidateModelAccess(ctx, resolution.ResolvedSelector); err != nil {
			return nil, err
		}
	}
	if resolution == nil {
		resolution, err = ResolveRequestModelWithAuthorizer(ctx, o.provider, o.modelResolver, o.modelAuthorizer, core.NewRequestedModelSelector(*model, *providerHint))
		if err != nil {
			return nil, err
		}
		workflow, err = TranslatedWorkflow(ctx, strings.TrimSpace(requestID), endpoint, resolution, o.workflowPolicyResolver)
		if err != nil {
			return nil, err
		}
	}
	ApplyResolvedSelector(model, providerHint, resolution)
	return workflow, nil
}

func currentTranslatedWorkflow(workflow *core.Workflow, endpoint core.EndpointDescriptor) *core.Workflow {
	if workflow == nil {
		return nil
	}
	if workflow.Mode != core.ExecutionModeTranslated || workflow.Endpoint.Operation != endpoint.Operation {
		return nil
	}
	return workflow
}

// ApplyResolvedSelector updates request model/provider fields to the resolved selector.
func ApplyResolvedSelector(model, providerHint *string, resolution *core.RequestModelResolution) {
	if model == nil || providerHint == nil || resolution == nil {
		return
	}
	*model = resolution.ResolvedSelector.Model
	*providerHint = resolution.ResolvedSelector.Provider
}

// WithCacheRequestContext injects workflow and guardrails cache metadata into ctx.
func (o *InferenceOrchestrator) WithCacheRequestContext(ctx context.Context, workflow *core.Workflow) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if workflow != nil {
		ctx = core.WithWorkflow(ctx, workflow)
	}
	if workflow != nil && workflow.Policy != nil {
		return core.WithGuardrailsHash(ctx, workflow.GuardrailsHash())
	}
	if o.guardrailsHash != "" {
		return core.WithGuardrailsHash(ctx, o.guardrailsHash)
	}
	return ctx
}
