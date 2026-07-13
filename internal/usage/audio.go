package usage

import (
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	endpointAudioSpeech         = "/v1/audio/speech"
	endpointAudioTranscriptions = "/v1/audio/transcriptions"

	// rawKeyInputCharacters and rawKeyAudioSeconds are the RawData keys that carry
	// the non-token billable units audio providers do not report as tokens: input
	// characters for text-to-speech and input audio duration for transcription.
	// cost.go prices them via PerCharacterInput / PerSecondInput respectively.
	rawKeyInputCharacters = "input_characters"
	rawKeyAudioSeconds    = "audio_seconds"

	// rawKeyAudioOutputSeconds carries the synthesized speech duration the gateway
	// measures from the returned audio, priced via PerSecondOutput. rawKeyAudioOutputFormat
	// records the output codec so cost.go can flag a duration it could not measure
	// (opus/aac/flac) rather than reporting a silent zero.
	rawKeyAudioOutputSeconds = "audio_output_seconds"
	rawKeyAudioOutputFormat  = "audio_output_format"
)

// ExtractFromSpeechRequest builds a usage entry for a text-to-speech request.
// Speech responses are binary audio with no provider-reported usage, so the
// billable units are derived locally: the input character count (per-character
// models such as tts-1) and the synthesized audio duration (per-second-output
// models such as gpt-4o-mini-tts), both recorded in RawData so the interaction
// stays observable and pricing can apply. output is the returned audio and
// format its response_format/MIME type; duration is measured for wav/pcm/mp3
// (see measureSpeechDurationSeconds). model is the resolved route
// model (not the raw user input) so the row groups and prices consistently with
// the pricing lookup, mirroring the transcription extractor.
func ExtractFromSpeechRequest(input string, output []byte, format, requestID, model, provider string, pricing ...*core.ModelPricing) *UsageEntry {
	entry := &UsageEntry{
		ID:        uuid.New().String(),
		RequestID: requestID,
		Timestamp: time.Now().UTC(),
		Model:     model,
		Provider:  provider,
		Endpoint:  endpointAudioSpeech,
	}

	raw := map[string]any{}
	if chars := len([]rune(input)); chars > 0 {
		raw[rawKeyInputCharacters] = chars
	}
	// Record the output codec whenever it is known, even for an empty body, so an
	// unmeasurable format still surfaces a cost caveat in cost.go rather than a
	// silent zero. Record the measured duration only when the gateway can compute
	// it from the returned audio (wav/pcm/mp3).
	if codec := normalizeAudioFormat(format); codec != "" {
		raw[rawKeyAudioOutputFormat] = codec
	}
	if len(output) > 0 {
		if seconds, ok := measureSpeechDurationSeconds(output, format); ok {
			raw[rawKeyAudioOutputSeconds] = seconds
		}
	}
	if len(raw) > 0 {
		entry.RawData = raw
	}

	applyUsageCosts(entry, provider, endpointAudioSpeech, pricing...)

	return entry
}

// transcriptionUsage mirrors the optional usage object the gpt-4o transcription
// models return. It is token-based or duration-based; whisper omits it entirely.
type transcriptionUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	Seconds      float64 `json:"seconds"`
}

// ExtractFromTranscriptionResponse builds a usage entry for a speech-to-text
// request. The response body is proxied verbatim; when it is JSON it may carry a
// usage object (token- or duration-based). The entry is always returned so the
// interaction stays observable even when the provider reports no usage (whisper,
// or non-JSON response formats such as text/srt/vtt).
func ExtractFromTranscriptionResponse(body []byte, requestID, model, provider string, pricing ...*core.ModelPricing) *UsageEntry {
	entry := &UsageEntry{
		ID:        uuid.New().String(),
		RequestID: requestID,
		Timestamp: time.Now().UTC(),
		Model:     model,
		Provider:  provider,
		Endpoint:  endpointAudioTranscriptions,
	}

	var parsed struct {
		Usage *transcriptionUsage `json:"usage"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Usage != nil {
		u := parsed.Usage
		entry.InputTokens = u.InputTokens
		entry.OutputTokens = u.OutputTokens
		entry.TotalTokens = u.TotalTokens
		if entry.TotalTokens == 0 {
			entry.TotalTokens = u.InputTokens + u.OutputTokens
		}
		if u.Seconds > 0 {
			entry.RawData = map[string]any{rawKeyAudioSeconds: u.Seconds}
		}
	}

	applyUsageCosts(entry, provider, endpointAudioTranscriptions, pricing...)

	return entry
}
