package elevenlabs

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

var _ core.AudioVoicesProvider = (*Provider)(nil)

// voicesPageSize is the largest page GET /v2/voices serves.
const voicesPageSize = 100

// maxVoicePages bounds how many pages one listing follows, so a workspace
// with a huge voice library cannot turn one call into an unbounded crawl.
const maxVoicePages = 20

type voicesPage struct {
	Voices        []json.RawMessage `json:"voices"`
	HasMore       bool              `json:"has_more"`
	NextPageToken string            `json:"next_page_token"`
}

// ListVoices lists the workspace's voices through GET /v2/voices, following
// pagination. ElevenLabs voices are not tied to a model, so every TTS model
// lists the same set; each voice_id is what /v1/audio/speech takes as voice.
// One API key serves every page and retry: with several keys configured, a
// next_page_token from one workspace is meaningless in another.
func (p *Provider) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio voices request is required", nil)
	}
	ctx = context.WithValue(ctx, pinnedKeyContextKey{}, p.keys.NextForContext(ctx))
	items := []json.RawMessage{}
	token := ""
	for range maxVoicePages {
		query := url.Values{"page_size": {strconv.Itoa(voicesPageSize)}, "include_total_count": {"false"}}
		if token != "" {
			query.Set("next_page_token", token)
		}
		var resp voicesPage
		if err := p.client.Do(ctx, llmclient.Request{
			Method:   http.MethodGet,
			Endpoint: "/v2/voices?" + query.Encode(),
			Model:    req.Model,
		}, &resp); err != nil {
			return nil, refineElevenLabsError(err)
		}
		items = append(items, resp.Voices...)
		token = strings.TrimSpace(resp.NextPageToken)
		if !resp.HasMore || token == "" {
			break
		}
	}
	body, err := json.Marshal(items)
	if err != nil {
		return nil, core.NewProviderError("elevenlabs", http.StatusBadGateway, "failed to read voices response", err)
	}
	return core.AudioVoicesFromBody("elevenlabs", body)
}
