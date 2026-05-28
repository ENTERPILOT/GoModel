package core

import "testing"

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
