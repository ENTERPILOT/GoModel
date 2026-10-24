package jev

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/systemone"
)

var _ core.DecisionsProvider = (*Provider)(nil)

// kevImagesRefusal refuses images on a Kev server. The gateway refuses images
// for models it knows to be text only, but a model configured without
// listing (models_mode: allowlist) carries no capability, so the provider
// checks too: a Kev server would read the image references as text.
const kevImagesRefusal = "this jev provider points at a Kev server, which reads text only; send images to TypeSafe's hosted API (api.typesafe.ai) or another model that reads images"

// Decisions answers an OpenAI Decisions request with System One; see
// systemone.AnswerDecisions. Only TypeSafe's hosted API reads images.
func (p *Provider) Decisions(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	opts := systemone.Options{ProviderType: "jev"}
	if !p.hosted.Load() {
		opts.ImagesRefusal = kevImagesRefusal
	}
	return systemone.AnswerDecisions(ctx, req, p.Passthrough, opts)
}
