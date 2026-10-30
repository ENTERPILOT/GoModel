package azure

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/systemone"
)

var _ core.DecisionsProvider = (*Provider)(nil)

const (
	// systemOnePath is where Foundry serves Microsoft's decision models
	// (Microsoft-Decision-1): the System One API on the resource root,
	// outside /openai, with no api-version.
	systemOnePath = "/providers/microsoft/v1/systemone"
	// openAIDecisionsPath is where Foundry serves OpenAI's Decisions API
	// (gpt-6-luna), on the unversioned v1 surface of the resource root.
	openAIDecisionsPath = "/openai/v1/decisions"
)

// decisionImagesRefusal refuses images: Microsoft-Decision-1 reads text and
// JSON state only.
const decisionImagesRefusal = "Azure's Microsoft-Decision-1 reads text and JSON only; send images to a gpt-6-luna deployment or a hosted jev model"

// Decisions answers an OpenAI Decisions request. A request the gateway marks
// for System One (core.DecisionsSystemOneEndpoint), for a Microsoft-Decision-1
// deployment, is translated to System One and the answer back; see
// systemone.AnswerDecisions. Every other request, for a gpt-6-luna
// deployment, is forwarded as sent to Foundry's Decisions endpoint, whatever
// base URL form is configured.
func (p *Provider) Decisions(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("decisions request is required", nil)
	}
	if req.Endpoint == core.DecisionsSystemOneEndpoint {
		return systemone.AnswerDecisions(ctx, req, p.Passthrough, systemone.Options{ProviderType: "azure", ImagesRefusal: decisionImagesRefusal})
	}
	forwarded := *req
	forwarded.Endpoint = openAIDecisionsPath
	return p.resourceProvider.Passthrough(ctx, &forwarded)
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

// bufferDecisionBody gives a decision request body a known length, since
// Foundry refuses chunked bodies (no_content_length_header). Decision
// requests are small. A failed read is kept, so the client reports it when
// sending.
func bufferDecisionBody(req *llmclient.Request) {
	if req.RawBodyReader == nil {
		return
	}
	body, err := io.ReadAll(req.RawBodyReader)
	if closer, ok := req.RawBodyReader.(io.Closer); ok {
		_ = closer.Close()
	}
	if err != nil {
		req.RawBodyReader = io.MultiReader(bytes.NewReader(body), errReader{err})
		return
	}
	req.RawBody, req.RawBodyReader = body, nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func isSystemOneEndpoint(endpoint string) bool {
	path := providers.PassthroughEndpoint(endpoint)
	path = strings.TrimPrefix(path, "/v1")
	return path == "/systemone"
}
