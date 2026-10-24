package openai

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

var _ core.DecisionsProvider = (*Provider)(nil)

// Decisions forwards an OpenAI Decisions request to /decisions as sent. It
// is defined on Provider rather than CompatibleProvider: the Decisions API is
// OpenAI's own, and the OpenAI-compatible upstreams built on the shared
// adapter do not serve it.
func (p *Provider) Decisions(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("decisions request is required", nil)
	}
	forwarded := *req
	forwarded.Endpoint = "decisions"
	return p.Passthrough(ctx, &forwarded)
}
