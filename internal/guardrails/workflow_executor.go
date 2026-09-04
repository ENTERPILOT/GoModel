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
	prompt, err := from(req)
	if err != nil {
		return zero, core.NewInvalidRequestError("invalid "+kind+" request for guardrails", err)
	}
	edited, err := newPromptRun(ctx, chain).run(ctx, prompt)
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
