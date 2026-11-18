package openai

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// openAIVoices lists OpenAI's built-in text-to-speech voices, in the order of
// the Text to speech guide
// (https://developers.openai.com/api/docs/guides/text-to-speech). OpenAI's
// voice-listing endpoint returns only custom voices, so this table must be
// updated when OpenAI adds voices. legacy marks the voices tts-1 and tts-1-hd
// also support.
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

// ListVoices returns the voices of an OpenAI speech model: the built-in ones
// from the table above, then the organization's custom voices from the
// upstream. The custom voices are an addition to a list that is useful without
// them, so when the upstream call fails (a key without the api.voices.read
// permission, an outage) the built-in voices are still returned and the
// failure is logged (see customVoicesFailureLevel). A model that is not a speech model reports voice listing
// as unsupported: realtime and chat-audio models take a different voice set.
// A provider pointed at a custom base URL (a self-hosted server) forwards to
// its /audio/voices instead.
func (p *Provider) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil || !isOpenAIHost(p.GetBaseURL()) {
		return p.CompatibleProvider.ListVoices(ctx, req)
	}
	voices := builtinVoices(req.Model)
	if voices == nil {
		return nil, core.NewAudioVoicesUnsupportedError(req.Model)
	}
	custom, err := p.customVoices(ctx, req.Model)
	if err != nil {
		slog.Log(ctx, customVoicesFailureLevel(err), "openai custom voice listing failed; returning the voices read so far",
			"provider", p.providerName, "custom_voices", len(custom), "error", err)
	}
	return &core.AudioVoicesResponse{Voices: append(voices, custom...)}, nil
}

// builtinVoices returns the built-in voices a speech model supports: the
// smaller set for the tts-1 family (tts-1, tts-1-hd and their snapshots), all
// of them for the other speech models, which carry "tts" in their name
// (gpt-4o-mini-tts and its snapshots). It returns nil for any other model.
func builtinVoices(model string) []core.AudioVoice {
	m := strings.ToLower(strings.TrimSpace(model))
	legacyOnly := m == "tts-1" || strings.HasPrefix(m, "tts-1-")
	if !legacyOnly && !strings.Contains(m, "tts") {
		return nil
	}
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
