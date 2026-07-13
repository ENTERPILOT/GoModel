package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
	if entry == nil {
		t.Fatal("expected a usage entry")
	}
	if entry.Endpoint != endpointRealtime {
		t.Errorf("endpoint = %q, want %q", entry.Endpoint, endpointRealtime)
	}
	if entry.InputTokens != 100 || entry.OutputTokens != 50 || entry.TotalTokens != 150 {
		t.Errorf("tokens = (%d,%d,%d), want (100,50,150)", entry.InputTokens, entry.OutputTokens, entry.TotalTokens)
	}
	// Keys must match cost.go's priced rawData keys so audio is billed at audio rates.
	if entry.RawData["prompt_audio_tokens"] != 60 || entry.RawData["completion_audio_tokens"] != 30 {
		t.Errorf("audio token breakdown missing/miskeyed: %v", entry.RawData)
	}
	if entry.RawData["prompt_cached_tokens"] != 10 {
		t.Errorf("cached tokens missing/miskeyed: %v", entry.RawData)
	}
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
	if entry == nil || entry.TotalCost == nil {
		t.Fatal("expected a costed entry")
	}
	const wantInput, wantOutput = 7.2e-06, 0.0020496
	if got := *entry.InputCost; !floatNear(got, wantInput) {
		t.Errorf("input cost = %g, want %g", got, wantInput)
	}
	if got := *entry.OutputCost; !floatNear(got, wantOutput) {
		t.Errorf("output cost = %g, want %g (29 text@2.40 + 99 audio@20)", got, wantOutput)
	}
	if got := *entry.TotalCost; !floatNear(got, wantInput+wantOutput) {
		t.Errorf("total cost = %g, want %g", got, wantInput+wantOutput)
	}
	if entry.CostsCalculationCaveat != "" {
		t.Errorf("unexpected caveat: %q", entry.CostsCalculationCaveat)
	}
}

func floatNear(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-12
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
	if entry == nil {
		t.Fatal("expected entry")
	}
	if entry.TotalTokens != 203 {
		t.Errorf("total = %d, want 203", entry.TotalTokens)
	}
	if entry.RawData["completion_audio_tokens"] != 9 {
		t.Errorf("plural output audio tokens not captured: %v", entry.RawData)
	}
	if entry.RawData["prompt_text_tokens"] != 192 {
		t.Errorf("plural input text tokens not captured: %v", entry.RawData)
	}
}

func TestExtractFromRealtimeResponseDoneTotalsFallback(t *testing.T) {
	payload := []byte(`{"type":"response.done","response":{"usage":{"input_tokens":7,"output_tokens":3}}}`)
	entry := ExtractFromRealtimeResponseDone(payload, "r", "m", "openai")
	if entry == nil {
		t.Fatal("expected entry")
	}
	if entry.TotalTokens != 10 {
		t.Errorf("total = %d, want 10 (derived)", entry.TotalTokens)
	}
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
			if entry := ExtractFromRealtimeResponseDone(payload, "r", "m", "openai"); entry != nil {
				t.Errorf("expected nil entry, got %+v", entry)
			}
		})
	}
}
