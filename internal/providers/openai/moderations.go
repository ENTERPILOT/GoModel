package openai

import (
	"context"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

var _ core.ModerationProvider = (*Provider)(nil)

// CreateModeration forwards a moderation request to OpenAI's /moderations
// endpoint. It is defined on Provider rather than CompatibleProvider: the
// OpenAI-compatible upstreams built on the shared adapter (Azure, OpenRouter,
// ...) do not serve it.
func (p *Provider) CreateModeration(ctx context.Context, req *core.ModerationRequest) (*core.ModerationResponse, error) {
	if err := core.ValidateModerationRequest(req); err != nil {
		return nil, err
	}
	var resp core.ModerationResponse
	err := p.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/moderations",
		Model:     req.Model,
		Operation: llmclient.OperationModerations,
		Body:      req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Results == nil {
		resp.Results = []core.ModerationResult{}
	}
	return &resp, nil
}
