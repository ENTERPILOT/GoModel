package providers

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// CountMessagesTokens routes a Messages token count to the provider that
// owns the model. A provider without a counting endpoint answers
// core.ErrMessagesTokenCountUnsupported so the caller can estimate instead.
func (r *Router) CountMessagesTokens(ctx context.Context, model string, body []byte) (int, error) {
	count, _, err := routeResolvedModelCall(
		r, ctx, model, "",
		func(route resolvedRoute) string { return route.selector.Model },
		func(ctx context.Context, provider core.Provider, resolvedModel string) (int, error) {
			counter, ok := provider.(core.MessagesTokenCounter)
			if !ok {
				return 0, core.ErrMessagesTokenCountUnsupported
			}
			return counter.CountMessagesTokens(ctx, resolvedModel, body)
		},
	)
	return count, err
}

// CountChatTokens routes a chat token count to the provider that owns the
// model. The request is forwarded as for a completion (model resolved, other
// providers' replay state and unsupported cache directives removed), so the
// count covers what the provider would actually receive.
func (r *Router) CountChatTokens(ctx context.Context, req *core.ChatRequest) (int, error) {
	if req == nil {
		return 0, core.NewInvalidRequestError("chat request is required", nil)
	}
	count, _, err := routeResolvedModelCall(
		r, ctx, req.Model, req.Provider,
		func(route resolvedRoute) *core.ChatRequest { return forwardChatRequest(ctx, req, route) },
		func(ctx context.Context, provider core.Provider, forward *core.ChatRequest) (int, error) {
			counter, ok := provider.(core.ChatTokenCounter)
			if !ok {
				return 0, core.ErrMessagesTokenCountUnsupported
			}
			return counter.CountChatTokens(ctx, forward)
		},
	)
	return count, err
}
