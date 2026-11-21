package providers

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// CreateModeration routes a moderation request to the provider that owns the
// model, requiring it to implement core.ModerationProvider.
func (r *Router) CreateModeration(ctx context.Context, req *core.ModerationRequest) (*core.ModerationResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("moderation request is required", nil)
	}
	return routeStampedModelResponse(
		r, ctx, req.Model, req.Provider,
		func(route resolvedRoute) *core.ModerationRequest {
			forwardReq := *req
			forwardReq.Model = route.selector.Model
			forwardReq.Provider = ""
			return &forwardReq
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ModerationRequest) (*core.ModerationResponse, error) {
			moderator, ok := provider.(core.ModerationProvider)
			if !ok {
				return nil, core.NewInvalidRequestError("model \""+req.Model+"\" does not support moderations; use an openai or mistral moderation model", nil)
			}
			return moderator.CreateModeration(ctx, forwardReq)
		},
	)
}
