package azure

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/systemone"
)

var (
	_ core.DecisionsProvider   = (*Provider)(nil)
	_ core.DecisionImageReader = (*Provider)(nil)
)

// systemOnePath is where Foundry serves Microsoft's decision models
// (Microsoft-Decision-1): the System One API on the resource root, outside
// /openai, with no api-version.
const systemOnePath = "/providers/microsoft/v1/systemone"

// decisionImagesRefusal refuses images: Microsoft-Decision-1 reads text and
// JSON state only.
const decisionImagesRefusal = "Azure's decision models (Microsoft-Decision-1) read text and JSON only; send images to openai/gpt-6-luna or a hosted jev model"

// Decisions answers an OpenAI Decisions request for an Azure decision
// deployment (Microsoft-Decision-1), which speaks System One: the request is
// translated to System One and the answer back; see
// systemone.AnswerDecisions. The gateway sends only decision models here,
// not Azure's chat deployments.
func (p *Provider) Decisions(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return systemone.AnswerDecisions(ctx, req, p.Passthrough, systemone.Options{ProviderType: "azure", ImagesRefusal: decisionImagesRefusal})
}

// DecisionsReadImages reports that Azure's decision models read no images,
// so the gateway routes image requests elsewhere.
func (p *Provider) DecisionsReadImages() bool {
	return false
}

// Passthrough sends System One requests (POST /v1/systemone, and Decisions
// translated to System One) to Foundry's decision endpoint on the resource
// root, and every other endpoint to the configured base URL.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req != nil && isSystemOneEndpoint(req.Endpoint) {
		forwarded := *req
		forwarded.Endpoint = systemOnePath
		return p.resourceProvider.Passthrough(ctx, &forwarded)
	}
	return p.CompatibleProvider.Passthrough(ctx, req)
}

func isSystemOneEndpoint(endpoint string) bool {
	path := providers.PassthroughEndpoint(endpoint)
	path = strings.TrimPrefix(path, "/v1")
	return path == "/systemone"
}
