package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractFromSpeechRequest(t *testing.T) {
	entry := ExtractFromSpeechRequest("hello", nil, "", "req-1", "gpt-4o-mini-tts", "openai")
	require.NotNil(t, entry)
	assert.Equal(t, endpointAudioSpeech, entry.Endpoint)
	assert.Equal(t, "gpt-4o-mini-tts", entry.Model)
	assert.Equal(t, "openai", entry.Provider)
	assert.Equal(t, "req-1", entry.RequestID, "identity mismatch: %+v", entry)
	assert.Equal(t, 5, entry.RawData["input_characters"])
}

func TestExtractFromSpeechRequest_EmptyInput(t *testing.T) {
	entry := ExtractFromSpeechRequest("", nil, "", "req", "tts-1", "openai")
	require.NotNil(t, entry)
	assert.Nil(t, entry.RawData)
}

func TestExtractFromSpeechRequest_PerCharacterPricing(t *testing.T) {
	// "hello" = 5 characters at $0.00001/char => $0.00005.
	entry := ExtractFromSpeechRequest("hello", nil, "", "req", "tts-1", "openai", &core.ModelPricing{PerCharacterInput: new(0.00001)})
	assertCostNear(t, "input cost", entry.InputCost, 0.00005)
	assertCostNear(t, "total cost", entry.TotalCost, 0.00005)
}

func TestExtractFromSpeechRequest_WavOutputPerSecondOutput(t *testing.T) {
	// A 1-second 24 kHz mono 16-bit WAV at $0.00025/sec => $0.00025 output cost.
	wav := buildWAV(t, 24000, 1, 16, 1.0)
	entry := ExtractFromSpeechRequest("hello", wav, "wav", "req", "gpt-4o-mini-tts", "openai",
		&core.ModelPricing{InputPerMtok: new(0.6), PerSecondOutput: new(0.00025)})
	assert.Equal(t, float64(1), entry.RawData[rawKeyAudioOutputSeconds])

	assertCostNear(t, "output cost", entry.OutputCost, 0.00025)
	assertCostNear(t, "total cost", entry.TotalCost, 0.00025)
	assert.Empty(t, entry.CostsCalculationCaveat)
}

func TestExtractFromSpeechRequest_PcmOutputPerSecondOutput(t *testing.T) {
	// Headerless PCM is 48000 bytes/sec; 24000 bytes => 0.5s at $0.00025/sec.
	pcm := make([]byte, 24000)
	entry := ExtractFromSpeechRequest("hi", pcm, "pcm", "req", "gpt-4o-mini-tts", "openai",
		&core.ModelPricing{PerSecondOutput: new(0.00025)})
	assert.Equal(t, 0.5, entry.RawData[rawKeyAudioOutputSeconds])

	assertCostNear(t, "output cost", entry.OutputCost, 0.000125)
}

func TestExtractFromSpeechRequest_CompressedOutputCaveat(t *testing.T) {
	// mp3 output cannot be measured without decoding; the per-second-output model
	// must surface a caveat rather than a silent zero.
	entry := ExtractFromSpeechRequest("hello", []byte("\xff\xfbmp3 frames"), "mp3", "req", "gpt-4o-mini-tts", "openai",
		&core.ModelPricing{InputPerMtok: new(0.6), PerSecondOutput: new(0.00025)})
	_, ok := entry.RawData[rawKeyAudioOutputSeconds]
	assert.False(t, ok)
	assert.Equal(t, "mp3", entry.RawData[rawKeyAudioOutputFormat])
	assert.Contains(t, entry.CostsCalculationCaveat, "mp3")
}

func TestExtractFromTranscriptionResponse_TokenUsage(t *testing.T) {
	body := []byte(`{"text":"hi","usage":{"type":"tokens","input_tokens":14,"output_tokens":45,"total_tokens":59}}`)

	entry := ExtractFromTranscriptionResponse(body, nil, "req-2", "gpt-4o-transcribe", "openai")
	require.NotNil(t, entry)
	assert.Equal(t, 14, entry.InputTokens)
	assert.Equal(t, 45, entry.OutputTokens)
	assert.Equal(t, 59, entry.TotalTokens, "token counts mismatch: %+v", entry)
}

func TestExtractFromAudioTextResponse_UsesEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		extract  func() *UsageEntry
		endpoint string
	}{
		{
			name: "transcription",
			extract: func() *UsageEntry {
				return ExtractFromTranscriptionResponse([]byte(`{"text":"hello"}`), nil, "req", "whisper-1", "openai")
			},
			endpoint: endpointAudioTranscriptions,
		},
		{
			name: "translation",
			extract: func() *UsageEntry {
				return ExtractFromTranslationResponse([]byte(`{"text":"hello"}`), nil, "req", "whisper-1", "openai")
			},
			endpoint: endpointAudioTranslations,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.extract()
			assert.Equal(t, tt.endpoint, entry.Endpoint)
		})
	}
}

func TestExtractFromTranscriptionResponse_TotalTokensDerived(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":10,"output_tokens":20}}`)

	entry := ExtractFromTranscriptionResponse(body, nil, "req", "m", "openai")
	assert.Equal(t, 30, entry.TotalTokens)
}

func TestExtractFromTranscriptionResponse_DurationUsage(t *testing.T) {
	body := []byte(`{"text":"hi","usage":{"type":"duration","seconds":9}}`)

	entry := ExtractFromTranscriptionResponse(body, nil, "req", "whisper-1", "openai")
	assert.Equal(t, 0, entry.TotalTokens)
	assert.Equal(t, float64(9), entry.RawData["audio_seconds"])
}

func TestExtractFromTranscriptionResponse_PerSecondCost(t *testing.T) {
	// 9 seconds of input audio at $0.0001/sec => $0.0009 (whisper-style pricing).
	body := []byte(`{"usage":{"type":"duration","seconds":9}}`)
	pricing := &core.ModelPricing{PerSecondInput: new(0.0001)}

	entry := ExtractFromTranscriptionResponse(body, nil, "req", "whisper-1", "openai", pricing)
	assertCostNear(t, "input cost", entry.InputCost, 0.0009)
}

func TestExtractFromTranscriptionResponse_NoUsage(t *testing.T) {
	// Whisper / text responses carry no usage; the interaction is still recorded.
	tests := []struct {
		name string
		body []byte
	}{
		{"json without usage", []byte(`{"text":"hi"}`)},
		{"plain text", []byte("plain transcript text")},
		{"nil body", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := ExtractFromTranscriptionResponse(tt.body, nil, "req", "whisper-1", "openai")
			require.NotNil(t, entry)
			assert.Equal(t, 0, entry.TotalTokens)
			assert.Nil(t, entry.RawData, "expected zero-usage entry, got %+v", entry)
		})
	}
}

func TestExtractFromTranscriptionResponse_TokenCost(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":1000000,"output_tokens":0,"total_tokens":1000000}}`)
	pricing := &core.ModelPricing{InputPerMtok: new(2.5)}

	entry := ExtractFromTranscriptionResponse(body, nil, "req", "gpt-4o-transcribe", "openai", pricing)
	assertCostNear(t, "input cost", entry.InputCost, 2.5)
}

// TestTranscriptionBillableDuration pins the rule that the billable unit of a
// transcription is the audio's duration, however the provider reports it — and
// however the client asked for the transcript. response_format decides the
// shape of the body, never the price.
func TestTranscriptionBillableDuration(t *testing.T) {
	// whisper-1: $0.0001 per input second.
	pricing := &core.ModelPricing{PerSecondInput: new(0.0001)}
	wav := buildWAV(t, 24000, 1, 16, 2.5)

	tests := []struct {
		name        string
		body        []byte
		audio       []byte
		pricing     *core.ModelPricing
		wantSeconds float64
		wantCost    *float64
		wantCaveat  string
	}{
		{
			name:        "usage seconds are authoritative",
			body:        []byte(`{"text":"hi","usage":{"type":"duration","seconds":3}}`),
			audio:       wav,
			pricing:     pricing,
			wantSeconds: 3,
			wantCost:    new(0.0003),
		},
		{
			name:        "verbose_json duration is used when usage is absent",
			body:        []byte(`{"text":"hi","duration":2.5,"segments":[]}`),
			audio:       nil,
			pricing:     pricing,
			wantSeconds: 2.5,
			wantCost:    new(0.00025),
		},
		{
			name:        "a plain text transcript is priced from the uploaded audio",
			body:        []byte("hi"),
			audio:       wav,
			pricing:     pricing,
			wantSeconds: 2.5,
			wantCost:    new(0.00025),
		},
		{
			name:        "an srt transcript costs the same as json",
			body:        []byte("1\n00:00:00,000 --> 00:00:02,500\nhi\n"),
			audio:       wav,
			pricing:     pricing,
			wantSeconds: 2.5,
			wantCost:    new(0.00025),
		},
		{
			name:        "a provider that reports nothing is priced from the upload",
			body:        []byte(`{"text":"hi"}`),
			audio:       buildMP3(100),
			pricing:     &core.ModelPricing{PerSecondInput: new(0.00001111)},
			wantSeconds: 100 * mp3FrameSeconds,
			wantCost:    new(100 * mp3FrameSeconds * 0.00001111),
		},
		{
			name:       "an unmeasurable upload with no reported usage is flagged",
			body:       []byte(`{"text":"hi"}`),
			audio:      []byte("OggS not really measurable"),
			pricing:    pricing,
			wantCaveat: caveatAudioMissingUsage,
		},
		{
			name:    "a token-billed model keeps its token costs",
			body:    []byte(`{"text":"hi","usage":{"type":"tokens","input_tokens":1000000,"output_tokens":0}}`),
			audio:   wav,
			pricing: &core.ModelPricing{InputPerMtok: new(2.5)},
			// Tokens are the reported billable unit; no duration is recorded.
			wantCost: new(2.5),
		},
		{
			// A usage object carrying both units must not be charged twice:
			// whisper-1 publishes a token rate and a per-second rate for the
			// same audio.
			name:     "a usage object with tokens and seconds is billed by tokens",
			body:     []byte(`{"text":"hi","usage":{"input_tokens":1000000,"output_tokens":0,"seconds":9}}`),
			audio:    wav,
			pricing:  &core.ModelPricing{InputPerMtok: new(2.5), PerSecondInput: new(0.0001)},
			wantCost: new(2.5),
		},
		{
			// A token-billed response with an empty transcript states a zero
			// charge; measuring the upload instead would invent one.
			// The provider did report its usage, so the row is an
			// authoritative $0 rather than an unrecorded one.
			name:     "zero-count token usage is not re-priced by duration",
			body:     []byte(`{"text":"","usage":{"type":"tokens","input_tokens":0,"output_tokens":0}}`),
			audio:    wav,
			pricing:  &core.ModelPricing{InputPerMtok: new(2.5), PerSecondInput: new(0.0001)},
			wantCost: new(0.0),
		},
		{
			// A usage object naming duration as the billable unit but
			// reporting none is a genuine gap when the upload cannot be
			// measured either.
			name:       "zero-second duration usage with an unmeasurable upload is flagged",
			body:       []byte(`{"text":"","usage":{"type":"duration","seconds":0}}`),
			audio:      []byte("OggS not really measurable"),
			pricing:    pricing,
			wantCaveat: caveatAudioMissingUsage,
		},
		{
			name:       "a model priced only by audio input tokens is flagged when nothing is measurable",
			body:       []byte(`{"text":"hi"}`),
			audio:      []byte("OggS not really measurable"),
			pricing:    &core.ModelPricing{AudioInputPerMtok: new(6.0)},
			wantCaveat: caveatAudioMissingUsage,
		},
		{
			name:    "an unpriced model records the duration without a caveat",
			body:    []byte(`{"text":"hi"}`),
			audio:   wav,
			pricing: nil,

			wantSeconds: 2.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, extract := range []func() *UsageEntry{
				func() *UsageEntry {
					return ExtractFromTranscriptionResponse(tt.body, tt.audio, "req", "m", "openai", tt.pricing)
				},
				func() *UsageEntry {
					return ExtractFromTranslationResponse(tt.body, tt.audio, "req", "m", "openai", tt.pricing)
				},
			} {
				entry := extract()
				seconds, _ := extractFloat(entry.RawData, rawKeyAudioSeconds)
				assert.True(t, costsNearlyEqual(seconds, tt.wantSeconds), "audio_seconds = %v, want %v", seconds, tt.wantSeconds)

				if tt.wantCost == nil {
					assert.Nil(t, entry.TotalCost)
				} else {
					require.NotNil(t, entry.TotalCost)
					assert.True(t, costsNearlyEqual(*entry.TotalCost, *tt.wantCost), "total cost = %v, want %v", *entry.TotalCost, *tt.wantCost)
				}
				assert.Equal(t, tt.wantCaveat, entry.CostsCalculationCaveat)
			}
		})
	}
}
