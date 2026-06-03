package auditlog

import (
	"encoding/base64"
	"testing"
)

func TestIsAudioContentType(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"audio/mpeg", true},
		{"audio/wav", true},
		{"audio/mpeg; charset=utf-8", true},
		{"AUDIO/MPEG", true},
		{"application/json", false},
		{"", false},
		{"text/plain", false},
	}
	for _, tt := range tests {
		if got := IsAudioContentType(tt.contentType); got != tt.want {
			t.Errorf("IsAudioContentType(%q) = %v, want %v", tt.contentType, got, tt.want)
		}
	}
}

func TestBuildAudioResponseBody_StoresBase64WhenEnabled(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	body := BuildAudioResponseBody("audio/mpeg", data, true)

	if !body.Audio {
		t.Fatal("expected Audio marker to be true")
	}
	if !body.Stored || body.Encoding != "base64" {
		t.Fatalf("expected stored base64, got stored=%v encoding=%q", body.Stored, body.Encoding)
	}
	if body.Bytes != len(data) {
		t.Errorf("Bytes = %d, want %d", body.Bytes, len(data))
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		t.Fatalf("stored data is not valid base64: %v", err)
	}
	if string(decoded) != string(data) {
		t.Errorf("round-trip mismatch: audio bytes were not preserved losslessly")
	}
}

func TestBuildAudioResponseBody_PlaceholderWhenDisabled(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02}
	body := BuildAudioResponseBody("audio/mpeg", data, false)

	if !body.Audio {
		t.Fatal("expected Audio marker to be true")
	}
	if body.Stored || body.Data != "" || body.Encoding != "" {
		t.Errorf("expected no bytes stored when disabled, got %+v", body)
	}
	if body.Bytes != len(data) {
		t.Errorf("Bytes = %d, want %d (size metadata should still be recorded)", body.Bytes, len(data))
	}
}

func TestBuildAudioUploadBody_StoresBase64AndMeta(t *testing.T) {
	data := []byte("uploaded-audio")
	meta := map[string]any{"model": "gpt-4o-transcribe", "filename": "a.mp3"}
	body := BuildAudioUploadBody("audio/mpeg", data, true, meta)

	if !body.Audio || !body.Stored || body.Encoding != "base64" {
		t.Fatalf("expected stored base64 upload, got %+v", body)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || string(decoded) != "uploaded-audio" {
		t.Fatalf("base64 did not round-trip: decoded=%q err=%v", decoded, err)
	}
	if body.Meta["model"] != "gpt-4o-transcribe" {
		t.Errorf("meta not preserved alongside audio: %+v", body.Meta)
	}
}

func TestBuildAudioUploadBody_PlaceholderKeepsMeta(t *testing.T) {
	meta := map[string]any{"model": "whisper-1"}
	body := BuildAudioUploadBody("audio/wav", []byte("x"), false, meta)

	if body.Stored || body.Data != "" {
		t.Errorf("expected no bytes stored when disabled, got %+v", body)
	}
	if body.Meta["model"] != "whisper-1" {
		t.Errorf("meta should be kept on the placeholder: %+v", body.Meta)
	}
}

func TestBuildAudioResponseBody_TooLarge(t *testing.T) {
	data := make([]byte, audioBodyMaxBytes+1)
	body := BuildAudioResponseBody("audio/mpeg", data, true)

	if body.Stored || body.Data != "" {
		t.Error("expected oversized audio to not be stored")
	}
	if !body.TooLarge {
		t.Error("expected TooLarge to be set for oversized audio")
	}
}

func TestBuildAudioResponseBody_AvoidsUTF8Corruption(t *testing.T) {
	// MP3 frame headers contain bytes that are invalid UTF-8; the old capture
	// path coerced these to U+FFFD. base64 must preserve them exactly.
	data := []byte{0xff, 0xfb, 0x90, 0x00}
	body := BuildAudioResponseBody("audio/mpeg", data, true)
	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		t.Fatalf("invalid base64: %v", err)
	}
	// The bytes must survive verbatim — the old toValidUTF8String path would
	// have rewritten 0xff/0x90 into the U+FFFD replacement character (0xEF 0xBF
	// 0xBD), changing the byte length and content.
	if len(decoded) != len(data) {
		t.Fatalf("length changed: got %d bytes, want %d (UTF-8 coercion would have altered this)", len(decoded), len(data))
	}
	for i := range data {
		if decoded[i] != data[i] {
			t.Fatalf("byte %d = %#x, want %#x", i, decoded[i], data[i])
		}
	}
}
