package core

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

func TestSpeechResponseContentType(t *testing.T) {
	cases := map[string]string{
		"":       "audio/mpeg",
		"mp3":    "audio/mpeg",
		"MP3":    "audio/mpeg",
		"  wav ": "audio/wav",
		"opus":   "audio/ogg",
		"aac":    "audio/aac",
		"flac":   "audio/flac",
		"pcm":    "audio/pcm",
		"bogus":  "application/octet-stream",
	}
	for format, want := range cases {
		if got := SpeechResponseContentType(format); got != want {
			t.Errorf("SpeechResponseContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestTranscriptionResponseContentType(t *testing.T) {
	cases := map[string]string{
		"":             "application/json",
		"json":         "application/json",
		"verbose_json": "application/json",
		"text":         "text/plain; charset=utf-8",
		"srt":          "text/plain; charset=utf-8",
		"VTT":          "text/plain; charset=utf-8",
		"unknown":      "application/json",
	}
	for format, want := range cases {
		if got := TranscriptionResponseContentType(format); got != want {
			t.Errorf("TranscriptionResponseContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestDecodeAudioSpeechRequest(t *testing.T) {
	req, err := DecodeAudioSpeechRequest([]byte(`{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy","response_format":"wav","speed":1.5}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Model != "gpt-4o-mini-tts" || req.Input != "hi" || req.Voice != "alloy" || req.ResponseFormat != "wav" || req.Speed != 1.5 {
		t.Fatalf("decoded request mismatch: %+v", req)
	}

	if _, err := DecodeAudioSpeechRequest([]byte(`{"model":`), nil); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// TestAudioSpeechRequest_PreservesUnknownFields covers ADR-0011 rule 1 on
// /v1/audio/speech: a parameter the gateway has no typed field for (here
// OpenAI's stream_format) must survive the decode and reach the provider body.
func TestAudioSpeechRequest_PreservesUnknownFields(t *testing.T) {
	body := []byte(`{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy","stream_format":"sse","x_vendor":{"beta":true}}`)

	req, err := DecodeAudioSpeechRequest(body, nil)
	if err != nil {
		t.Fatalf("DecodeAudioSpeechRequest() error = %v", err)
	}
	if req.Model != "gpt-4o-mini-tts" || req.Input != "hi" || req.Voice != "alloy" {
		t.Fatalf("typed fields mismatch: %+v", req)
	}
	if got := string(req.ExtraFields.Lookup("stream_format")); got != `"sse"` {
		t.Errorf("stream_format extra = %s, want \"sse\"", got)
	}
	if got := string(req.ExtraFields.Lookup("x_vendor")); got != `{"beta":true}` {
		t.Errorf("x_vendor extra = %s", got)
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("Unmarshal(encoded) error = %v", err)
	}
	for field, want := range map[string]any{
		"model": "gpt-4o-mini-tts", "input": "hi", "voice": "alloy", "stream_format": "sse",
	} {
		if round[field] != want {
			t.Errorf("encoded %s = %v, want %v", field, round[field], want)
		}
	}
	// Typed members must not be duplicated into the extras on the way out.
	if strings.Count(string(encoded), `"model"`) != 1 {
		t.Errorf("model duplicated in encoded body: %s", encoded)
	}
}

// TestAudioSpeechRequest_KnownFieldsAreNotExtras guards the gateway-controlled
// members: a client cannot smuggle a second model/voice/provider through the
// passthrough object.
func TestAudioSpeechRequest_KnownFieldsAreNotExtras(t *testing.T) {
	req, err := DecodeAudioSpeechRequest([]byte(
		`{"model":"tts-1","input":"hi","voice":"alloy","instructions":"calm","response_format":"wav","speed":1.5,"provider":"openai"}`), nil)
	if err != nil {
		t.Fatalf("DecodeAudioSpeechRequest() error = %v", err)
	}
	if !req.ExtraFields.IsEmpty() {
		t.Fatalf("ExtraFields = %+v, want empty", req.ExtraFields)
	}
	if req.ResponseFormat != "wav" || req.Speed != 1.5 || req.Instructions != "calm" || req.Provider != "openai" {
		t.Fatalf("typed fields mismatch: %+v", req)
	}
}
