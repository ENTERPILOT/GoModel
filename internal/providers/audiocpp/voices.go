package audiocpp

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

var _ core.AudioVoicesProvider = (*Provider)(nil)

// ListVoices asks audio.cpp's GET /v1/audio/voices?model=... for the voices a
// TTS model offers and normalizes the answer.
func (p *Provider) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio voices request is required", nil)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	raw, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/audio/voices?" + url.Values{"model": {model}}.Encode(),
		Model:    model,
	})
	if err != nil {
		return nil, err
	}
	return core.AudioVoicesFromBody("audiocpp", raw.Body)
}
