package openai

import (
	"context"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// openAIVoices lists OpenAI's built-in text-to-speech voices, in the order of
// the Text to speech guide
// (https://developers.openai.com/api/docs/guides/text-to-speech). OpenAI has
// no voice-listing endpoint, so this table must be updated when OpenAI adds
// voices. legacy marks the voices tts-1 and tts-1-hd also support.
var openAIVoices = []struct {
	id     string
	legacy bool
}{
	{"alloy", true},
	{"ash", true},
	{"ballad", false},
	{"coral", true},
	{"echo", true},
	{"fable", true},
	{"nova", true},
	{"onyx", true},
	{"sage", true},
	{"shimmer", true},
	{"verse", false},
	{"marin", false},
	{"cedar", false},
}

// ListVoices returns OpenAI's built-in voices for model without calling the
// upstream, which has no voice-listing route. An organization's custom voices
// are not included: OpenAI has no API to list them. A provider pointed at a
// custom base URL (a self-hosted server) forwards to its /audio/voices instead.
func (p *Provider) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil || !isOpenAIHost(p.GetBaseURL()) {
		return p.CompatibleProvider.ListVoices(ctx, req)
	}
	return &core.AudioVoicesResponse{Voices: builtinVoices(req.Model)}, nil
}

// builtinVoices returns the built-in voices model supports: the smaller set
// for the tts-1 family (tts-1, tts-1-hd and their snapshots), all of them
// otherwise.
func builtinVoices(model string) []core.AudioVoice {
	m := strings.ToLower(strings.TrimSpace(model))
	legacyOnly := m == "tts-1" || strings.HasPrefix(m, "tts-1-")
	voices := make([]core.AudioVoice, 0, len(openAIVoices))
	for _, voice := range openAIVoices {
		if legacyOnly && !voice.legacy {
			continue
		}
		voices = append(voices, core.AudioVoice{ID: voice.id, Name: voice.id})
	}
	return voices
}

// isOpenAIHost reports whether baseURL is OpenAI's own API: api.openai.com or
// a regional host under it such as eu.api.openai.com.
func isOpenAIHost(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".api.openai.com")
}
