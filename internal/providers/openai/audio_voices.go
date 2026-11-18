package openai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// ListVoices forwards GET {base_url}/audio/voices?model=... to the upstream.
// Self-hosted TTS servers registered as openai providers (Kokoro-FastAPI,
// Speaches, ...) serve the route, so the shared adapter asks rather than
// guesses. An upstream without it (most hosted providers) answers 404 or 405,
// which is reported as the same unsupported-capability error a provider
// without voice listing returns, not as an upstream failure. OpenAI's own
// route lists only an organization's custom voices; Provider adds the
// built-in ones for it (see builtinVoices).
func (p *CompatibleProvider) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio voices request is required", nil)
	}
	model := strings.TrimSpace(req.Model)
	endpoint := "/audio/voices"
	if model != "" {
		endpoint += "?" + url.Values{"model": {model}}.Encode()
	}
	raw, err := p.client.DoRaw(ctx, p.prepareRequest(llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
		Model:    model,
	}))
	if err != nil {
		if voiceListingMissing(err) {
			return nil, core.NewAudioVoicesUnsupportedError(model)
		}
		return nil, err
	}
	return core.AudioVoicesFromBody(p.providerName, raw.Body)
}

// voiceListingMissing reports an upstream that has no voice-listing route.
func voiceListingMissing(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	switch gatewayErr.HTTPStatusCode() {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	return false
}
