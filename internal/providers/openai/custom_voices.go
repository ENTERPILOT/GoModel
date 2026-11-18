package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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

type customVoicesPage struct {
	// Data is a pointer so a body without the array is told apart from an
	// organization with no custom voices.
	Data *[]struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// customVoices lists the organization's custom voices usable for speech
// through GET /audio/voices, following its cursor. On an error, which includes
// a listing it could not read to the end, it returns the voices read so far. With several API keys configured, each page may go out
// under a different key; keys of one organization share the same voices.
func (p *Provider) customVoices(ctx context.Context, model string) ([]core.AudioVoice, error) {
	var voices []core.AudioVoice
	after := ""
	for range maxCustomVoicePages {
		query := url.Values{"limit": {strconv.Itoa(customVoicesPageSize)}}
		if after != "" {
			query.Set("after", after)
		}
		var page customVoicesPage
		if err := p.Do(ctx, llmclient.Request{
			Method:   http.MethodGet,
			Endpoint: "/audio/voices?" + query.Encode(),
			Model:    model,
		}, &page); err != nil {
			return voices, err
		}
		if page.Data == nil {
			return voices, core.ErrUnrecognizedVoices
		}
		for _, voice := range *page.Data {
			id := strings.TrimSpace(voice.ID)
			if id == "" || voice.Type == promptVoiceType {
				continue
			}
			name := strings.TrimSpace(voice.Name)
			if name == "" {
				name = id
			}
			voices = append(voices, core.AudioVoice{ID: id, Name: name})
		}
		if !page.HasMore {
			return voices, nil
		}
		if after = strings.TrimSpace(page.LastID); after == "" {
			return voices, errors.Join(core.ErrUnrecognizedVoices, errors.New("has_more without last_id"))
		}
	}
	return voices, fmt.Errorf("more than %d pages of custom voices", maxCustomVoicePages)
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
