package usage

import "testing"

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
