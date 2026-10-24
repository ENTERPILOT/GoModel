package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractFromRealtimeResponseDone(t *testing.T) {
	payload := []byte(`{
		"type": "response.done",
		"response": {
			"usage": {
				"total_tokens": 150,
				"input_tokens": 100,
				"output_tokens": 50,
				"input_token_details": {"text_tokens": 40, "audio_tokens": 60, "cached_tokens": 10},
				"output_token_details": {"text_tokens": 20, "audio_tokens": 30}
			}
		}
	}`)

	entry := ExtractFromRealtimeResponseDone(payload, "req-1", "gpt-realtime", "openai")
	require.NotNil(t, entry)
	assert.Equal(t, endpointRealtime, entry.Endpoint)
	assert.Equal(t, 100, entry.InputTokens)
	assert.Equal(t, 50, entry.OutputTokens)
	assert.Equal(t, 150, entry.TotalTokens)

	// Keys must match cost.go's priced rawData keys so audio is billed at audio rates.
	assert.Equal(t, 60, entry.RawData["prompt_audio_tokens"])
	assert.Equal(t, 30, entry.RawData["completion_audio_tokens"], "audio token breakdown missing/miskeyed: %v", entry.RawData)
	assert.Equal(t, 10, entry.RawData["prompt_cached_tokens"], "cached tokens missing/miskeyed: %v", entry.RawData)
}

func TestExtractFromRealtimeResponseDoneUSDCost(t *testing.T) {
	// Mirrors a live gpt-realtime-mini audio turn: 12 input text tokens, 128
	// output tokens (99 audio + 29 text). With base output $2.40/Mtok and audio
	// output $20/Mtok, audio must price at the audio rate (not base) and must not
	// be double-counted: 29*2.40/1e6 + 99*20/1e6 = 0.0020496.
	ptr := func(f float64) *float64 { return &f }
	pricing := &core.ModelPricing{
		InputPerMtok:       ptr(0.60),
		OutputPerMtok:      ptr(2.40),
		AudioInputPerMtok:  ptr(10.0),
		AudioOutputPerMtok: ptr(20.0),
	}
	payload := []byte(`{"type":"response.done","response":{"usage":{
		"input_tokens":12,"output_tokens":128,"total_tokens":140,
		"input_token_details":{"text_tokens":12},
		"output_token_details":{"text_tokens":29,"audio_tokens":99}
	}}}`)

	entry := ExtractFromRealtimeResponseDone(payload, "r", "gpt-realtime-mini", "openai", pricing)
	require.NotNil(t, entry)
	require.NotNil(t, entry.TotalCost)

	const wantInput, wantOutput = 7.2e-06, 0.0020496
	assert.InDelta(t, wantInput, *entry.InputCost, 1e-12, "input cost")
	assert.InDelta(t, wantOutput, *entry.OutputCost, 1e-12, "output cost (29 text@2.40 + 99 audio@20)")
	assert.InDelta(t, wantInput+wantOutput, *entry.TotalCost, 1e-12, "total cost")
	assert.Empty(t, entry.CostsCalculationCaveat)
}

func TestExtractFromRealtimeResponseDonePluralDetails(t *testing.T) {
	// Alibaba/Bailian uses the plural "*_tokens_details" spelling; audio tokens
	// must still be captured and priced.
	payload := []byte(`{
		"type": "response.done",
		"response": {"usage": {
			"input_tokens": 192, "output_tokens": 11, "total_tokens": 203,
			"input_tokens_details": {"text_tokens": 192},
			"output_tokens_details": {"text_tokens": 2, "audio_tokens": 9}
		}}
	}`)
	entry := ExtractFromRealtimeResponseDone(payload, "r", "qwen3-omni-flash-realtime", "bailian")
	require.NotNil(t, entry)
	assert.Equal(t, 203, entry.TotalTokens)
	assert.Equal(t, 9, entry.RawData["completion_audio_tokens"], "plural output audio tokens not captured: %v", entry.RawData)
	assert.Equal(t, 192, entry.RawData["prompt_text_tokens"], "plural input text tokens not captured: %v", entry.RawData)
}

func TestExtractFromRealtimeResponseDoneTotalsFallback(t *testing.T) {
	payload := []byte(`{"type":"response.done","response":{"usage":{"input_tokens":7,"output_tokens":3}}}`)
	entry := ExtractFromRealtimeResponseDone(payload, "r", "m", "openai")
	require.NotNil(t, entry)
	assert.Equal(t, 10, entry.TotalTokens)
}

func TestExtractFromRealtimeResponseDoneSkipsNonBillable(t *testing.T) {
	cases := map[string][]byte{
		"other event type":       []byte(`{"type":"response.audio.delta","delta":"abc"}`),
		"response.done no usage": []byte(`{"type":"response.done","response":{}}`),
		"invalid json":           []byte(`not json`),
		"empty":                  []byte(``),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			entry := ExtractFromRealtimeResponseDone(payload, "r", "m", "openai")
			assert.Nil(t, entry)
		})
	}
}

func TestExtractFromRealtimeTranscriptionCompleted(t *testing.T) {
	// The real event shape from a gpt-4o-transcribe transcription session.
	payload := []byte(`{
		"type": "conversation.item.input_audio_transcription.completed",
		"item_id": "item_1",
		"transcript": "Hello there.",
		"usage": {
			"type": "tokens",
			"total_tokens": 30,
			"input_tokens": 25,
			"output_tokens": 5,
			"input_token_details": {"text_tokens": 0, "audio_tokens": 25}
		}
	}`)

	entry := ExtractFromRealtimeTranscriptionCompleted(payload, "req-1", "gpt-4o-transcribe", "openai")
	require.NotNil(t, entry)
	assert.Equal(t, endpointRealtime, entry.Endpoint)
	assert.Equal(t, 25, entry.InputTokens)
	assert.Equal(t, 5, entry.OutputTokens)
	assert.Equal(t, 30, entry.TotalTokens)
	assert.Equal(t, 25, entry.RawData["prompt_audio_tokens"], "audio token breakdown missing/miskeyed: %v", entry.RawData)
}

func TestExtractFromRealtimeTranscriptionCompletedDuration(t *testing.T) {
	// whisper-1 reports duration usage instead of tokens: it must carry the
	// same rawData key as HTTP transcription so the per-second input rate
	// prices it. 2.5 s at $0.0001/s => $0.00025.
	payload := []byte(`{
		"type": "conversation.item.input_audio_transcription.completed",
		"usage": {"type": "duration", "seconds": 2.5}
	}`)
	pricing := &core.ModelPricing{PerSecondInput: new(0.0001)}

	entry := ExtractFromRealtimeTranscriptionCompleted(payload, "req-1", "whisper-1", "openai", pricing)
	require.NotNil(t, entry)
	assert.Equal(t, 0, entry.TotalTokens)
	assert.Equal(t, 2.5, entry.RawData[rawKeyAudioSeconds], "audio seconds missing/miskeyed: %v", entry.RawData)

	assertCostNear(t, "input cost", entry.InputCost, 0.00025)
}

func TestNewRealtimeDurationEntry(t *testing.T) {
	// Sessions the gateway meters itself (OpenAI translation sessions report no
	// usage events) price the same way as provider-reported duration usage:
	// 90 s at $0.00056667/s => $0.051.
	pricing := &core.ModelPricing{PerSecondInput: new(0.00056667)}

	entry := NewRealtimeDurationEntry(90, "req-1", "gpt-realtime-translate", "openai", pricing)
	require.NotNil(t, entry)
	assert.Equal(t, endpointRealtime, entry.Endpoint)
	assert.Equal(t, "gpt-realtime-translate", entry.Model)
	assert.Equal(t, "openai", entry.Provider, "entry = %+v, want the session's model and provider", entry)
	assert.Equal(t, 0, entry.TotalTokens)
	assert.Equal(t, float64(90), entry.RawData[rawKeyAudioSeconds], "audio seconds missing/miskeyed: %v", entry.RawData)

	assertCostNear(t, "input cost", entry.InputCost, 0.0510003)
}

func TestExtractFromRealtimeTranscriptionCompletedSkipsNonBillable(t *testing.T) {
	for name, payload := range map[string]string{
		"delta event":     `{"type":"conversation.item.input_audio_transcription.delta","delta":"He"}`,
		"missing usage":   `{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hi."}`,
		"malformed frame": `{"type":`,
	} {
		entry := ExtractFromRealtimeTranscriptionCompleted([]byte(payload), "req-1", "m", "openai")
		assert.Nil(t, entry, "payload %q", name)
	}
}

func TestHasBillableUsage(t *testing.T) {
	// The gateway meters a session's relayed audio only when the session itself
	// reported nothing billable, so a zero-value report must not read as a bill.
	tests := []struct {
		name  string
		entry *UsageEntry
		want  bool
	}{
		{name: "nil entry"},
		{name: "tokens", entry: &UsageEntry{TotalTokens: 30, InputTokens: 25}, want: true},
		{name: "output tokens only", entry: &UsageEntry{OutputTokens: 5}, want: true},
		{name: "audio seconds", entry: &UsageEntry{RawData: map[string]any{rawKeyAudioSeconds: 2.5}}, want: true},
		{name: "zero tokens", entry: &UsageEntry{}},
		{name: "zero seconds", entry: &UsageEntry{RawData: map[string]any{rawKeyAudioSeconds: 0.0}}},
		{name: "unrelated raw data", entry: &UsageEntry{RawData: map[string]any{"prompt_text_tokens": 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasBillableUsage(tt.entry))
		})
	}
}
