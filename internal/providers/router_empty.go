package providers

import (
	"context"
	"errors"
	"log/slog"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// SetEmptyResponseHook registers the observer for provider responses that
// return 200 without choices, output, or usage.
func (r *Router) SetEmptyResponseHook(hook func(context.Context, llmclient.EmptyResponseInfo)) {
	r.onEmptyResponse = hook
}

// observeEmptyResponse logs and reports a provider call that succeeded at the
// HTTP level but returned nothing usable. The gateway still decides whether
// that is an error for the client (no choices fails over; no usage does not).
func (r *Router) observeEmptyResponse(ctx context.Context, route resolvedRoute, resp any, err error) {
	reason := emptyResponseReason(resp, err)
	if reason == "" {
		return
	}
	info := llmclient.EmptyResponseInfo{
		Provider:     route.selector.Provider,
		ProviderType: route.providerType,
		Model:        route.selector.Model,
		Operation:    llmclient.OperationChat,
		Reason:       reason,
	}
	slog.WarnContext(ctx, "provider returned 200 with an empty response",
		"provider", info.Provider,
		"provider_type", info.ProviderType,
		"model", info.Model,
		"reason", info.Reason,
	)
	if r.onEmptyResponse != nil {
		r.onEmptyResponse(ctx, info)
	}
}

// emptyResponseReason classifies buffered chat and Responses API results.
// Other response types, and calls that failed for any reason other than an
// empty 200, are never reported.
func emptyResponseReason(resp any, err error) string {
	if err != nil {
		if errors.Is(err, core.ErrNoChoices) {
			return llmclient.EmptyReasonNoChoices
		}
		return ""
	}
	switch typed := resp.(type) {
	case *core.ChatResponse:
		if typed == nil || len(typed.Choices) == 0 {
			return llmclient.EmptyReasonNoChoices
		}
		if typed.Usage.PromptTokens == 0 && typed.Usage.CompletionTokens == 0 && typed.Usage.TotalTokens == 0 {
			return llmclient.EmptyReasonNoUsage
		}
	case *core.ResponsesResponse:
		if typed == nil {
			return llmclient.EmptyReasonNoOutput
		}
		// Background and in-progress responses legitimately carry neither
		// output nor usage yet.
		if typed.Status != "" && typed.Status != "completed" {
			return ""
		}
		if len(typed.Output) == 0 {
			return llmclient.EmptyReasonNoOutput
		}
		if typed.Usage == nil || (typed.Usage.InputTokens == 0 && typed.Usage.OutputTokens == 0 && typed.Usage.TotalTokens == 0) {
			return llmclient.EmptyReasonNoUsage
		}
	}
	return ""
}
