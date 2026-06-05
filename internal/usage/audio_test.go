package usage

import (
	"testing"

	"gomodel/internal/core"
)

func floatPtr(v float64) *float64 { return &v }

func TestExtractFromSpeechRequest(t *testing.T) {
	entry := ExtractFromSpeechRequest("hello", "req-1", "gpt-4o-mini-tts", "openai")
	if entry == nil {
		t.Fatal("expected a usage entry")
	}
	if entry.Endpoint != endpointAudioSpeech {
		t.Errorf("endpoint = %q, want %q", entry.Endpoint, endpointAudioSpeech)
	}
	if entry.Model != "gpt-4o-mini-tts" || entry.Provider != "openai" || entry.RequestID != "req-1" {
		t.Errorf("identity mismatch: %+v", entry)
	}
	if got := entry.RawData["input_characters"]; got != 5 {
		t.Errorf("input_characters = %v, want 5", got)
	}
}

func TestExtractFromSpeechRequest_EmptyInput(t *testing.T) {
	entry := ExtractFromSpeechRequest("", "req", "tts-1", "openai")
	if entry == nil {
		t.Fatal("empty input should still yield an entry")
	}
	if entry.RawData != nil {
		t.Errorf("empty input should record no characters, got %+v", entry.RawData)
	}
}

func TestExtractFromSpeechRequest_PerCharacterPricing(t *testing.T) {
	// "hello" = 5 characters at $0.00001/char => $0.00005.
	entry := ExtractFromSpeechRequest("hello", "req", "tts-1", "openai", &core.ModelPricing{PerCharacterInput: floatPtr(0.00001)})
	assertCostPtrNear(t, "input cost", entry.InputCost, 0.00005)
	assertCostPtrNear(t, "total cost", entry.TotalCost, 0.00005)
}

func TestExtractFromSpeechRequest_NoPerCharacterRateStaysUnpriced(t *testing.T) {
	// gpt-4o-mini-tts is priced by output audio duration, which the gateway does
	// not measure; with only an output-side audio rate, character usage is unpriced.
	entry := ExtractFromSpeechRequest("hello", "req", "gpt-4o-mini-tts", "openai", &core.ModelPricing{PerSecondOutput: floatPtr(0.00025)})
	if entry.TotalCost != nil {
		t.Errorf("want nil cost when only output audio duration is priced, got %v", *entry.TotalCost)
	}
}

func TestExtractFromTranscriptionResponse_TokenUsage(t *testing.T) {
	body := []byte(`{"text":"hi","usage":{"type":"tokens","input_tokens":14,"output_tokens":45,"total_tokens":59}}`)

	entry := ExtractFromTranscriptionResponse(body, "req-2", "gpt-4o-transcribe", "openai")
	if entry == nil {
		t.Fatal("expected a usage entry")
	}
	if entry.Endpoint != endpointAudioTranscriptions {
		t.Errorf("endpoint = %q, want %q", entry.Endpoint, endpointAudioTranscriptions)
	}
	if entry.InputTokens != 14 || entry.OutputTokens != 45 || entry.TotalTokens != 59 {
		t.Errorf("token counts mismatch: %+v", entry)
	}
}

func TestExtractFromTranscriptionResponse_TotalTokensDerived(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":10,"output_tokens":20}}`)

	entry := ExtractFromTranscriptionResponse(body, "req", "m", "openai")
	if entry.TotalTokens != 30 {
		t.Errorf("total_tokens = %d, want 30 (derived)", entry.TotalTokens)
	}
}

func TestExtractFromTranscriptionResponse_DurationUsage(t *testing.T) {
	body := []byte(`{"text":"hi","usage":{"type":"duration","seconds":9}}`)

	entry := ExtractFromTranscriptionResponse(body, "req", "whisper-1", "openai")
	if entry.TotalTokens != 0 {
		t.Errorf("duration usage should report no tokens, got %d", entry.TotalTokens)
	}
	if got := entry.RawData["audio_seconds"]; got != float64(9) {
		t.Errorf("audio_seconds = %v, want 9", got)
	}
}

func TestExtractFromTranscriptionResponse_PerSecondCost(t *testing.T) {
	// 9 seconds of input audio at $0.0001/sec => $0.0009 (whisper-style pricing).
	body := []byte(`{"usage":{"type":"duration","seconds":9}}`)
	pricing := &core.ModelPricing{PerSecondInput: floatPtr(0.0001)}

	entry := ExtractFromTranscriptionResponse(body, "req", "whisper-1", "openai", pricing)
	assertCostPtrNear(t, "input cost", entry.InputCost, 0.0009)
}

func TestExtractFromTranscriptionResponse_NoUsage(t *testing.T) {
	// Whisper / text responses carry no usage; the interaction is still recorded.
	for _, body := range [][]byte{
		[]byte(`{"text":"hi"}`),
		[]byte("plain transcript text"),
		nil,
	} {
		entry := ExtractFromTranscriptionResponse(body, "req", "whisper-1", "openai")
		if entry == nil {
			t.Fatalf("expected an entry for body %q", body)
		}
		if entry.TotalTokens != 0 || entry.RawData != nil {
			t.Errorf("expected zero-usage entry for body %q, got %+v", body, entry)
		}
	}
}

func TestExtractFromTranscriptionResponse_TokenCost(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":1000000,"output_tokens":0,"total_tokens":1000000}}`)
	pricing := &core.ModelPricing{InputPerMtok: floatPtr(2.5)}

	entry := ExtractFromTranscriptionResponse(body, "req", "gpt-4o-transcribe", "openai", pricing)
	assertCostPtrNear(t, "input cost", entry.InputCost, 2.5)
}
