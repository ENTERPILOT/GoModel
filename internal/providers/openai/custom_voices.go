package openai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// customVoicesPageSize is the largest page OpenAI's GET /audio/voices serves.
const customVoicesPageSize = 100

// maxCustomVoicePages bounds one listing. OpenAI allows an organization 120
// custom voices, which is two pages; the rest is headroom for a raised limit.
const maxCustomVoicePages = 5

// customVoiceIDPrefix starts every custom voice ID; no built-in voice has it.
const customVoiceIDPrefix = "voice_"

// promptVoiceType marks a voice generated from a text prompt. The speech
// endpoint does not accept those, only voices created from a recording.
const promptVoiceType = "prompt"

// customVoicesPage is one page of OpenAI's GET /audio/voices.
type customVoicesPage struct {
	// Data is a pointer so a body without the array is told apart from an
	// organization with no custom voices.
	Data    *[]core.AudioCustomVoice `json:"data"`
	FirstID *string                  `json:"first_id"`
	LastID  *string                  `json:"last_id"`
	HasMore bool                     `json:"has_more"`
}

// fetchCustomVoices reads one page of the organization's custom voices.
func (p *Provider) fetchCustomVoices(ctx context.Context, model string, params core.AudioCustomVoicesParams) (*customVoicesPage, error) {
	query := url.Values{}
	if params.Limit != "" {
		query.Set("limit", params.Limit)
	}
	if params.After != "" {
		query.Set("after", params.After)
	}
	endpoint := "/audio/voices"
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var page customVoicesPage
	if err := p.Do(ctx, llmclient.Request{Method: http.MethodGet, Endpoint: endpoint, Model: model}, &page); err != nil {
		return nil, err
	}
	if page.Data == nil {
		return nil, core.NewProviderError(p.providerName, http.StatusBadGateway, p.providerName+" returned an unrecognized voices response", core.ErrUnrecognizedVoices)
	}
	return &page, nil
}

// ServesCustomVoices reports whether this provider talks to OpenAI's own API,
// which has the custom voice list. A provider pointed at a custom base URL (a
// self-hosted server) does not.
func (p *Provider) ServesCustomVoices() bool {
	return isOpenAIHost(p.GetBaseURL())
}

// ListCustomVoices serves OpenAI's own voice list: one cursor page of the
// organization's custom voices, as GET /audio/voices returns it, prompt-
// generated voices included.
func (p *Provider) ListCustomVoices(ctx context.Context, params core.AudioCustomVoicesParams) (*core.AudioCustomVoicesResponse, error) {
	if !p.ServesCustomVoices() {
		return nil, core.NewAudioCustomVoicesUnsupportedError(p.providerName)
	}
	page, err := p.fetchCustomVoices(ctx, "", params)
	if err != nil {
		return nil, err
	}
	return &core.AudioCustomVoicesResponse{
		Object:  "list",
		Data:    *page.Data,
		FirstID: page.FirstID,
		LastID:  page.LastID,
		HasMore: page.HasMore,
	}, nil
}

// customVoicesTimeout bounds the whole custom voice lookup. It is an addition
// to a list that is useful without it, so a stalled upstream must not hold the
// built-in voices back for the ordinary upstream timeout.
const customVoicesTimeout = 10 * time.Second

// customVoices lists the organization's custom voices usable for speech
// through GET /audio/voices, following its cursor. On an error, which includes
// a listing it could not read to the end, it returns the voices read so far.
// One API key serves every page: with several keys configured, a cursor or a
// permission of one key need not hold for another. timeout bounds the whole
// lookup, credential resolution included. A failing lookup cannot open the
// provider's circuit breaker: the router calls ListVoices outside it.
func (p *Provider) customVoices(ctx context.Context, model string, timeout time.Duration) ([]core.AudioVoice, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Pinned under the deadline: resolving a federated credential can block.
	ctx = p.withPinnedKey(ctx)
	var voices []core.AudioVoice
	seen := map[string]struct{}{}
	after := ""
	for range maxCustomVoicePages {
		page, err := p.fetchCustomVoices(ctx, model, core.AudioCustomVoicesParams{Limit: strconv.Itoa(customVoicesPageSize), After: after})
		if err != nil {
			return voices, err
		}
		for _, voice := range *page.Data {
			id := strings.TrimSpace(voice.ID)
			if _, dup := seen[id]; dup || id == "" || voice.Type == promptVoiceType {
				continue
			}
			seen[id] = struct{}{}
			name := strings.TrimSpace(voice.Name)
			if name == "" {
				name = id
			}
			voices = append(voices, core.AudioVoice{ID: id, Name: name})
		}
		if !page.HasMore {
			return voices, nil
		}
		next := ""
		if page.LastID != nil {
			next = strings.TrimSpace(*page.LastID)
		}
		if next == "" || next == after {
			return voices, errors.Join(core.ErrUnrecognizedVoices, errors.New("has_more without a new last_id"))
		}
		after = next
	}
	return voices, fmt.Errorf("more than %d pages of custom voices", maxCustomVoicePages)
}

// customVoicesFailureLevel picks the log level for a failed custom voice
// listing. A refused credential is a standing condition, usually an API key
// restricted on purpose, that would otherwise repeat a warning on every
// listing; anything else is unexpected.
func customVoicesFailureLevel(err error) slog.Level {
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		switch gatewayErr.HTTPStatusCode() {
		case http.StatusUnauthorized, http.StatusForbidden:
			return slog.LevelInfo
		}
	}
	return slog.LevelWarn
}

// withCustomVoiceObject returns req, or a copy that sends a custom voice ID
// given as a string in the object form. OpenAI rejects such an ID as a string,
// and clients that pick a voice from /v1/audio/voices send strings.
func withCustomVoiceObject(req *core.AudioSpeechRequest) *core.AudioSpeechRequest {
	if req == nil || !strings.HasPrefix(req.Voice, customVoiceIDPrefix) {
		return req
	}
	return req.WithObjectVoice()
}
