package minimax

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

var _ core.AudioVoicesProvider = (*Provider)(nil)

type voicesResponse struct {
	SystemVoice     json.RawMessage `json:"system_voice"`
	VoiceCloning    json.RawMessage `json:"voice_cloning"`
	VoiceGeneration json.RawMessage `json:"voice_generation"`
	BaseResponse    *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// ListVoices lists the account's system, cloned, and generated voices through
// MiniMax's POST /get_voice. Voices are not tied to a speech model, so every
// model lists the same set; each voice_id is what /v1/audio/speech takes as
// voice.
func (p *Provider) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio voices request is required", nil)
	}
	upstream, err := p.Passthrough(ctx, &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/get_voice",
		Body:     io.NopCloser(strings.NewReader(`{"voice_type":"all"}`)),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	if err != nil {
		return nil, err
	}
	if upstream == nil || upstream.Body == nil {
		return nil, core.NewEmptyProviderResponseError("minimax")
	}
	defer func() { _ = upstream.Body.Close() }()

	body, err := io.ReadAll(upstream.Body)
	if err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to read voices response", err)
	}
	if upstream.StatusCode < http.StatusOK || upstream.StatusCode >= http.StatusMultipleChoices {
		return nil, core.ParseProviderError("minimax", upstream.StatusCode, body, nil)
	}
	var resp voicesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to parse voices response", err)
	}
	if resp.BaseResponse == nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "voices response contains no status", nil)
	}
	if resp.BaseResponse.StatusCode != 0 {
		return nil, statusError("voices", resp.BaseResponse.StatusCode, resp.BaseResponse.StatusMsg, body)
	}

	// Parse the three categories as one list, so a voice ID that appears in
	// more than one keeps a single entry.
	items := []json.RawMessage{}
	for _, list := range []json.RawMessage{resp.SystemVoice, resp.VoiceCloning, resp.VoiceGeneration} {
		if list = bytes.TrimSpace(list); len(list) == 0 || bytes.Equal(list, []byte("null")) {
			continue
		}
		var category []json.RawMessage
		if err := json.Unmarshal(list, &category); err != nil {
			return nil, core.NewProviderError("minimax", http.StatusBadGateway, "minimax returned an unrecognized voices response", err)
		}
		items = append(items, category...)
	}
	combined, err := json.Marshal(items)
	if err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to read voices response", err)
	}
	return core.AudioVoicesFromBody("minimax", combined)
}
