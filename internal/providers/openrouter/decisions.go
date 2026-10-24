package openrouter

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/systemone"
)

var (
	_ core.DecisionsProvider   = (*Provider)(nil)
	_ core.DecisionImageReader = (*Provider)(nil)
)

// imagesRefusal refuses images: OpenRouter's System One route is not known
// to accept TypeSafe's image assets, so its decision models read text here.
const imagesRefusal = "OpenRouter's decision models read text only through GoModel; send images to openai/gpt-6-luna or a hosted jev model"

// Decisions answers an OpenAI Decisions request for one of OpenRouter's
// System One decision models (Jev, Kev), which OpenRouter serves at the same
// path and with the same shapes as TypeSafe: the request is translated to
// System One and the answer back; see systemone.AnswerDecisions. The gateway
// sends only decision models here, not OpenRouter's chat models.
func (p *Provider) Decisions(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return systemone.AnswerDecisions(ctx, req, p.Passthrough, systemone.Options{ProviderType: "openrouter", ImagesRefusal: imagesRefusal})
}

// DecisionsReadImages reports that OpenRouter's decision models read no
// images through GoModel, so the gateway routes image requests elsewhere.
func (p *Provider) DecisionsReadImages() bool {
	return false
}
