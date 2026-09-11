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
