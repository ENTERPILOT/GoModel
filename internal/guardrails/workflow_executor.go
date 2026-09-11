package guardrails

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/pluginapi"
)

// ContextChainsResolver resolves the request-scoped plugin chains.
type ContextChainsResolver interface {
	ChainsForContext(ctx context.Context) *plugins.Chains
}

// WorkflowRequestPatcher runs the prompt chain selected by the current
// workflow over translated requests.
type WorkflowRequestPatcher struct {
	resolver ContextChainsResolver
}

// NewWorkflowRequestPatcher creates a translated-request patcher that resolves
// its chains from the request context on each call.
func NewWorkflowRequestPatcher(resolver ContextChainsResolver) *WorkflowRequestPatcher {
	return &WorkflowRequestPatcher{resolver: resolver}
}

// PatchChatRequest runs the prompt chain over a translated chat request.
func (p *WorkflowRequestPatcher) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return processGuardedChat(ctx, p.chain(ctx), req)
}

// PatchResponsesRequest runs the prompt chain over a translated responses request.
func (p *WorkflowRequestPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return processGuardedResponses(ctx, p.chain(ctx), req)
}

// EditsPromptContent reports whether the request's prompt chain holds an
// instance that edits content, such as an anonymizing guardrail. A chained
// Responses request only needs its stored history expanded into the input
// when such an instance runs; otherwise the request reaches the provider as
// the client sent it. An instance configured only to flag or block counts as
// non-editing.
func (p *WorkflowRequestPatcher) EditsPromptContent(ctx context.Context) bool {
	for _, instance := range p.chain(ctx).Instances() {
		if instance.EditsContent() {
			return true
		}
	}
	return false
}

func (p *WorkflowRequestPatcher) chain(ctx context.Context) *plugins.Chain {
	return promptChain(p.resolver, ctx)
}

func promptChain(resolver ContextChainsResolver, ctx context.Context) *plugins.Chain {
	if resolver == nil {
		return nil
	}
	chains := resolver.ChainsForContext(ctx)
	if chains == nil {
		return nil
	}
	return chains.Prompt
}

func processGuardedChat(ctx context.Context, chain *plugins.Chain, req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return nil, nil
	}
	return processGuarded(ctx, chain, req, "chat", exchange.FromChatRequest, exchange.ApplyToChatRequest)
}

func processGuardedResponses(ctx context.Context, chain *plugins.Chain, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	if req == nil {
		return nil, nil
	}
	return processGuarded(ctx, chain, req, "responses", exchange.FromResponsesRequest, exchange.ApplyToResponsesRequest)
}

// processGuarded maps req to a prompt, runs the chain and applies the edits
// back. The request is returned as-is when nothing changed.
func processGuarded[Req any](
	ctx context.Context,
	chain *plugins.Chain,
	req Req,
	kind string,
	from func(Req) (*pluginapi.Prompt, error),
	apply func(Req, *pluginapi.Prompt) (Req, error),
) (Req, error) {
	var zero Req
	if chain.Empty() {
		return req, nil
	}
	chain.Acquire()
	defer chain.Release()
	prompt, err := from(req)
	if err != nil {
		return zero, core.NewInvalidRequestError("invalid "+kind+" request for guardrails", err)
	}
	run := newPromptRun(ctx, chain)
	// When the request is audited, every editing step leaves a PromptEdit
	// behind: a snapshot of the prompt as the step left it, applied to the
	// request later, off the request path, by the audit revision chain.
	var observe plugins.EditObserver
	if plugins.PromptEditCaptureEnabled(ctx) {
		observe = func(instance string, x *pluginapi.Exchange) {
			snapshot := x.Prompt.Clone()
			run.state.AddPromptEdit(plugins.PromptEdit{Instance: instance, Apply: func() (any, error) {
				applied, err := apply(req, snapshot)
				if err != nil {
					return nil, err
				}
				return applied, nil
			}})
		}
	}
	edited, err := run.run(ctx, prompt, observe)
	if err != nil {
		return zero, err
	}
	if !edited {
		return req, nil
	}
	applied, err := apply(req, prompt)
	if err != nil {
		return zero, core.NewInvalidRequestError("guardrails produced an invalid "+kind+" request: "+err.Error(), err)
	}
	return applied, nil
}
