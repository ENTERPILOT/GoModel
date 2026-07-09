package usage

import (
	"strings"
	"testing"

	"gomodel/internal/core"
)

func TestExtractFromSpeechRequest(t *testing.T) {
	entry := ExtractFromSpeechRequest("hello", nil, "", "req-1", "gpt-4o-mini-tts", "openai")
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
	entry := ExtractFromSpeechRequest("", nil, "", "req", "tts-1", "openai")
	if entry == nil {
		t.Fatal("empty input should still yield an entry")
	}
	if entry.RawData != nil {
		t.Errorf("empty input should record no characters, got %+v", entry.RawData)
	}
}

func TestExtractFromSpeechRequest_PerCharacterPricing(t *testing.T) {
	// "hello" = 5 characters at $0.00001/char => $0.00005.
	entry := ExtractFromSpeechRequest("hello", nil, "", "req", "tts-1", "openai", &core.ModelPricing{PerCharacterInput: new(0.00001)})
	assertCostPtrNear(t, "input cost", entry.InputCost, 0.00005)
	assertCostPtrNear(t, "total cost", entry.TotalCost, 0.00005)
}

func TestExtractFromSpeechRequest_WavOutputPerSecondOutput(t *testing.T) {
	// A 1-second 24 kHz mono 16-bit WAV at $0.00025/sec => $0.00025 output cost.
	wav := buildWAV(t, 24000, 1, 16, 1.0)
	entry := ExtractFromSpeechRequest("hello", wav, "wav", "req", "gpt-4o-mini-tts", "openai",
		&core.ModelPricing{InputPerMtok: new(0.6), PerSecondOutput: new(0.00025)})

	if got := entry.RawData[rawKeyAudioOutputSeconds]; got != float64(1) {
		t.Errorf("audio_output_seconds = %v, want 1", got)
	}
	assertCostPtrNear(t, "output cost", entry.OutputCost, 0.00025)
	assertCostPtrNear(t, "total cost", entry.TotalCost, 0.00025)
	if entry.CostsCalculationCaveat != "" {
		t.Errorf("measured wav should carry no caveat, got %q", entry.CostsCalculationCaveat)
	}
}

func TestExtractFromSpeechRequest_PcmOutputPerSecondOutput(t *testing.T) {
	// Headerless PCM is 48000 bytes/sec; 24000 bytes => 0.5s at $0.00025/sec.
	pcm := make([]byte, 24000)
	entry := ExtractFromSpeechRequest("hi", pcm, "pcm", "req", "gpt-4o-mini-tts", "openai",
		&core.ModelPricing{PerSecondOutput: new(0.00025)})

	if got := entry.RawData[rawKeyAudioOutputSeconds]; got != 0.5 {
		t.Errorf("audio_output_seconds = %v, want 0.5", got)
	}
	assertCostPtrNear(t, "output cost", entry.OutputCost, 0.000125)
}

func TestExtractFromSpeechRequest_CompressedOutputCaveat(t *testing.T) {
	// mp3 output cannot be measured without decoding; the per-second-output model
	// must surface a caveat rather than a silent zero.
	entry := ExtractFromSpeechRequest("hello", []byte("\xff\xfbmp3 frames"), "mp3", "req", "gpt-4o-mini-tts", "openai",
		&core.ModelPricing{InputPerMtok: new(0.6), PerSecondOutput: new(0.00025)})

	if _, ok := entry.RawData[rawKeyAudioOutputSeconds]; ok {
		t.Error("mp3 output should not record a measured duration")
	}
	if entry.RawData[rawKeyAudioOutputFormat] != "mp3" {
		t.Errorf("audio_output_format = %v, want mp3", entry.RawData[rawKeyAudioOutputFormat])
	}
	if !strings.Contains(entry.CostsCalculationCaveat, "mp3") {
		t.Errorf("want caveat mentioning mp3, got %q", entry.CostsCalculationCaveat)
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
	pricing := &core.ModelPricing{PerSecondInput: new(0.0001)}

	entry := ExtractFromTranscriptionResponse(body, "req", "whisper-1", "openai", pricing)
	assertCostPtrNear(t, "input cost", entry.InputCost, 0.0009)
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
			entry := ExtractFromTranscriptionResponse(tt.body, "req", "whisper-1", "openai")
			if entry == nil {
				t.Fatal("expected an entry")
			}
			if entry.TotalTokens != 0 || entry.RawData != nil {
				t.Errorf("expected zero-usage entry, got %+v", entry)
			}
		})
	}
}

func TestExtractFromTranscriptionResponse_TokenCost(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":1000000,"output_tokens":0,"total_tokens":1000000}}`)
	pricing := &core.ModelPricing{InputPerMtok: new(2.5)}

	entry := ExtractFromTranscriptionResponse(body, "req", "gpt-4o-transcribe", "openai", pricing)
	assertCostPtrNear(t, "input cost", entry.InputCost, 2.5)
}
