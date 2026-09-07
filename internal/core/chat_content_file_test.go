package core

import (
	"testing"

	"github.com/goccy/go-json"
)

func TestContentPartFileRoundTrip(t *testing.T) {
	raw := `{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0=","filename":"report.pdf"},"cache_control":{"type":"ephemeral"}}`
	var part ContentPart
	if err := json.Unmarshal([]byte(raw), &part); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if part.Type != "file" || part.File == nil || part.File.FileData != "data:application/pdf;base64,JVBERi0=" || part.File.Filename != "report.pdf" {
		t.Fatalf("part = %+v", part)
	}
	if got := string(part.ExtraFields.Lookup("cache_control")); got != `{"type":"ephemeral"}` {
		t.Errorf("cache_control = %s", got)
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	file, _ := decoded["file"].(map[string]any)
	if decoded["type"] != "file" || file["file_data"] != "data:application/pdf;base64,JVBERi0=" || file["filename"] != "report.pdf" {
		t.Errorf("encoded = %s", encoded)
	}
	if _, ok := decoded["cache_control"]; !ok {
		t.Errorf("encoded lost cache_control: %s", encoded)
	}
}

func TestContentPartFileRequiresPayload(t *testing.T) {
	for _, raw := range []string{
		`{"type":"file"}`,
		`{"type":"file","file":{}}`,
		`{"type":"file","file":{"filename":"x.pdf"}}`,
	} {
		var part ContentPart
		if err := json.Unmarshal([]byte(raw), &part); err == nil {
			t.Errorf("Unmarshal(%s) = nil error, want payload error", raw)
		}
	}
	var part ContentPart
	if err := json.Unmarshal([]byte(`{"type":"input_file","file":{"file_id":"file_123"}}`), &part); err != nil {
		t.Fatalf("Unmarshal file_id: %v", err)
	}
	if part.Type != "file" || part.File.FileID != "file_123" {
		t.Errorf("part = %+v", part)
	}
}

func TestNormalizeMessageContentFilePart(t *testing.T) {
	normalized, err := NormalizeMessageContent([]any{
		map[string]any{"type": "text", "text": "read this"},
		map[string]any{"type": "file", "file": map[string]any{"file_id": "file_123", "filename": "a.pdf"}},
	})
	if err != nil {
		t.Fatalf("NormalizeMessageContent: %v", err)
	}
	parts, ok := normalized.([]ContentPart)
	if !ok || len(parts) != 2 || parts[1].Type != "file" || parts[1].File == nil || parts[1].File.FileID != "file_123" {
		t.Fatalf("normalized = %#v", normalized)
	}
	if ExtractTextContent(parts) != "read this" {
		t.Errorf("ExtractTextContent = %q", ExtractTextContent(parts))
	}
}

func TestContentPartFileURLRoundTrip(t *testing.T) {
	raw := `{"type":"input_file","file":{"file_url":"https://example.com/a.pdf","filename":"a.pdf","x_file":1}}`
	var part ContentPart
	if err := json.Unmarshal([]byte(raw), &part); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if part.Type != "file" || part.File == nil || part.File.FileURL != "https://example.com/a.pdf" || part.File.FileData != "" {
		t.Fatalf("part = %+v", part)
	}
	if got := string(part.File.ExtraFields.Lookup("x_file")); got != "1" {
		t.Errorf("x_file = %s, want 1", got)
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded struct {
		File map[string]any `json:"file"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.File["file_url"] != "https://example.com/a.pdf" || decoded.File["x_file"] != float64(1) {
		t.Errorf("encoded = %s", encoded)
	}
	if _, ok := decoded.File["file_data"]; ok {
		t.Errorf("encoded emitted empty file_data: %s", encoded)
	}
}
