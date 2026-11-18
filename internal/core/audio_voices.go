package core

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
)

// AudioVoicesRequest asks for the voices a text-to-speech model offers
// (GET /v1/audio/voices?model=...). It is a GoModel extension, not part of
// the OpenAI API.
type AudioVoicesRequest struct {
	Model string
	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string
}

// AudioVoice is one voice a text-to-speech model offers. ID is the value a
// client passes as voice to /v1/audio/speech; Name is a display label that
// falls back to ID.
type AudioVoice struct {
	ID   string `json:"id" example:"af_bella"`
	Name string `json:"name" example:"Bella"`
}

// AudioVoicesResponse is the normalized voice list every provider returns.
// The shape is the one Open WebUI and Kokoro-FastAPI use.
type AudioVoicesResponse struct {
	Voices []AudioVoice `json:"voices"`
}

// AudioCustomVoicesParams are the cursor parameters of OpenAI's own voice list
// (GET /v1/audio/voices without a model). They are forwarded as sent, so the
// upstream is what validates them.
type AudioCustomVoicesParams struct {
	Limit string
	After string
}

// AudioCustomVoice is one custom voice in OpenAI's own voice list.
type AudioCustomVoice struct {
	ID        string `json:"id" example:"voice_1234"`
	Object    string `json:"object" example:"audio.voice"`
	Name      string `json:"name" example:"Narrator"`
	CreatedAt int64  `json:"created_at" example:"1734220800"`
	// Type is how the voice was created: "audio_sample" or "prompt".
	Type   string `json:"type,omitempty" example:"audio_sample"`
	Prompt string `json:"prompt,omitempty"`
}

// AudioCustomVoicesResponse is OpenAI's own voice list: one cursor page of an
// organization's custom voices.
type AudioCustomVoicesResponse struct {
	Object  string             `json:"object" example:"list"`
	Data    []AudioCustomVoice `json:"data"`
	FirstID *string            `json:"first_id"`
	LastID  *string            `json:"last_id"`
	HasMore bool               `json:"has_more"`
}

// ErrAudioCustomVoicesUnsupported marks a provider that does not serve
// OpenAI's own voice list.
var ErrAudioCustomVoicesUnsupported = errors.New("custom voice listing is not supported")

// NewAudioCustomVoicesUnsupportedError reports that provider does not serve
// OpenAI's own voice list.
func NewAudioCustomVoicesUnsupportedError(provider string) *GatewayError {
	return NewInvalidRequestError(fmt.Sprintf("provider %q does not support listing custom voices", provider), ErrAudioCustomVoicesUnsupported).WithParam("provider")
}

// NewAudioVoicesUnsupportedError reports that the provider behind model cannot
// list voices: it has no voice-listing capability, or its upstream has no
// such endpoint. It is the same invalid_request_error a provider without any
// other optional capability returns.
func NewAudioVoicesUnsupportedError(model string) *GatewayError {
	return NewInvalidRequestError(fmt.Sprintf("model %q does not support listing voices", model), nil).WithParam("model")
}

// ErrUnrecognizedVoices reports an upstream voice list in a shape
// ParseAudioVoices does not accept.
var ErrUnrecognizedVoices = errors.New("unrecognized voices response")

// ParseAudioVoices normalizes the voice lists self-hosted and hosted TTS
// servers return into AudioVoice values. It accepts a bare array, or an object
// carrying the array under "voices", "data", or "items". Array items are voice
// ID strings, or objects naming the voice by "id", "voice_id", or "name" with
// an optional display "name" or "voice_name" (only "voice_name" when "name"
// is the ID). An object item with no ID but its own "voices" array (LocalAI
// groups voices per model this way) is flattened. Items with no ID are
// skipped, repeated IDs keep their first entry, and every other field is
// dropped.
func ParseAudioVoices(body []byte) ([]AudioVoice, error) {
	items, err := voiceItems(body)
	if err != nil {
		return nil, err
	}
	voices := make([]AudioVoice, 0, len(items))
	collectVoices(items, &voices, make(map[string]struct{}, len(items)))
	return voices, nil
}

// AudioVoicesFromBody parses an upstream voice list into the normalized
// response, reporting a shape it does not accept as a 502 from provider.
func AudioVoicesFromBody(provider string, body []byte) (*AudioVoicesResponse, error) {
	voices, err := ParseAudioVoices(body)
	if err != nil {
		return nil, NewProviderError(provider, http.StatusBadGateway, provider+" returned an unrecognized voices response", err)
	}
	return &AudioVoicesResponse{Voices: voices}, nil
}

func voiceItems(body []byte) ([]json.RawMessage, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, ErrUnrecognizedVoices
	}
	switch body[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, errors.Join(ErrUnrecognizedVoices, err)
		}
		return items, nil
	case '{':
		var envelope struct {
			Voices json.RawMessage `json:"voices"`
			Data   json.RawMessage `json:"data"`
			Items  json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, errors.Join(ErrUnrecognizedVoices, err)
		}
		for _, list := range []json.RawMessage{envelope.Voices, envelope.Data, envelope.Items} {
			if list = bytes.TrimSpace(list); len(list) > 0 && list[0] == '[' {
				return voiceItems(list)
			}
		}
	}
	return nil, ErrUnrecognizedVoices
}

func collectVoices(items []json.RawMessage, voices *[]AudioVoice, seen map[string]struct{}) {
	for _, item := range items {
		voice, nested, ok := parseVoiceItem(item)
		if !ok {
			continue
		}
		if voice.ID == "" {
			collectVoices(nested, voices, seen)
			continue
		}
		if _, dup := seen[voice.ID]; dup {
			continue
		}
		seen[voice.ID] = struct{}{}
		*voices = append(*voices, voice)
	}
}

// parseVoiceItem reads one list item. ok is false for an item that is neither
// a voice nor a group; a group comes back as an empty voice with its nested
// items.
func parseVoiceItem(item json.RawMessage) (voice AudioVoice, nested []json.RawMessage, ok bool) {
	var id string
	if err := json.Unmarshal(item, &id); err == nil {
		id = strings.TrimSpace(id)
		return AudioVoice{ID: id, Name: id}, nil, id != ""
	}
	var fields struct {
		ID        string            `json:"id"`
		VoiceID   string            `json:"voice_id"`
		Name      string            `json:"name"`
		VoiceName string            `json:"voice_name"`
		Voices    []json.RawMessage `json:"voices"`
	}
	if err := json.Unmarshal(item, &fields); err != nil {
		return AudioVoice{}, nil, false
	}
	if id = firstNonBlank(fields.ID, fields.VoiceID); id != "" {
		return AudioVoice{ID: id, Name: firstNonBlank(fields.Name, fields.VoiceName, id)}, nil, true
	}
	// With no explicit ID, name is the ID and voice_name the display label.
	if id = firstNonBlank(fields.Name); id != "" {
		return AudioVoice{ID: id, Name: firstNonBlank(fields.VoiceName, id)}, nil, true
	}
	return AudioVoice{}, fields.Voices, len(fields.Voices) > 0
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
