package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// TestAudioTranscription_ForwardsUnknownFormFields covers ADR-0011 rule 1 on the
// multipart audio path: form values the gateway does not consume itself (here
// include[], chunking_strategy and a repeated vendor extra) reach the provider,
// while the parts the gateway controls stay out of the passthrough list.
func TestAudioTranscription_ForwardsUnknownFormFields(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider:      &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		transcriptionResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hi"}`)},
	}
	handler := NewHandler(mock, nil, nil, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, field := range [][2]string{
		{"model", "gpt-4o-transcribe"},
		{"response_format", "json"},
		{"prompt", "GoModel"},
		{"temperature", "0"},
		{"language", "en"},
		{"timestamp_granularities[]", "word"},
		{"include[]", "logprobs"},
		{"chunking_strategy", "auto"},
		{"x_vendor", "a"},
		{"x_vendor", "b"},
	} {
		if err := w.WriteField(field[0], field[1]); err != nil {
			t.Fatalf("WriteField(%s): %v", field[0], err)
		}
	}
	part, err := w.CreateFormFile("file", "speech.mp3")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = part.Write([]byte("audio-bytes"))
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioTranscriptions(c); err != nil {
		t.Fatalf("AudioTranscriptions returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	captured := mock.capturedTranscription
	if captured == nil {
		t.Fatal("provider was not called")
	}
	want := []core.FormField{
		{Name: "chunking_strategy", Value: "auto"},
		{Name: "include[]", Value: "logprobs"},
		{Name: "x_vendor", Value: "a"},
		{Name: "x_vendor", Value: "b"},
	}
	if len(captured.Fields) != len(want) {
		t.Fatalf("forwarded fields = %+v, want %+v", captured.Fields, want)
	}
	for i, field := range want {
		if captured.Fields[i] != field {
			t.Errorf("forwarded field %d = %+v, want %+v", i, captured.Fields[i], field)
		}
	}
	// The gateway-owned parts keep their typed home and never travel twice.
	if captured.Language != "en" || captured.Prompt != "GoModel" || captured.ResponseFormat != "json" {
		t.Errorf("typed fields mismatch: %+v", captured)
	}
}

// TestPassthroughFormFields_SkipsReservedNames pins the exclusion list so a
// client-supplied value can never overwrite a gateway-controlled multipart part.
func TestPassthroughFormFields_SkipsReservedNames(t *testing.T) {
	form := &multipart.Form{Value: map[string][]string{
		"model": {"evil"}, "file": {"evil"}, "provider": {"evil"},
		"language": {"en"}, "prompt": {"p"}, "response_format": {"json"},
		"temperature": {"0"}, "timestamp_granularities": {"word"},
		"timestamp_granularities[]": {"segment"},
		"include[]":                 {"logprobs"},
	}}
	got := passthroughFormFields(form)
	if len(got) != 1 || got[0] != (core.FormField{Name: "include[]", Value: "logprobs"}) {
		t.Fatalf("passthroughFormFields() = %+v, want only include[]", got)
	}
	if passthroughFormFields(nil) != nil {
		t.Error("passthroughFormFields(nil) should be nil")
	}
}

// TestAudioTranscriptionAuditInput_RecordsFieldNamesOnly keeps arbitrary
// client input out of the audit record: a forwarded field may carry a
// provider-native credential, so only its name is stored.
func TestAudioTranscriptionAuditInput_RecordsFieldNamesOnly(t *testing.T) {
	meta := audioTranscriptionAuditInput(&core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.wav",
		File:     []byte("audio"),
		Fields: []core.FormField{
			{Name: "include[]", Value: "logprobs"},
			{Name: "x_api_key", Value: "super-secret"},
			{Name: "x_api_key", Value: "super-secret-2"},
		},
	})
	names, ok := meta["forwarded_fields"].([]string)
	if !ok || len(names) != 2 || names[0] != "include[]" || names[1] != "x_api_key" {
		t.Fatalf("forwarded_fields = %v, want the distinct names in request order", meta["forwarded_fields"])
	}
	for key, value := range meta {
		if str, isString := value.(string); isString && strings.Contains(str, "super-secret") {
			t.Fatalf("audit meta %q leaked a forwarded value: %q", key, str)
		}
	}
	if _, present := meta["x_api_key"]; present {
		t.Error("forwarded field was recorded as its own audit key")
	}
}

// TestAudioSpeech_ForwardsUnknownJSONFields is the JSON half of ADR-0011 rule 1:
// an unknown speech parameter survives the service layer and reaches the
// provider request.
func TestAudioSpeech_ForwardsUnknownJSONFields(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")},
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy","stream_format":"sse"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedSpeech == nil {
		t.Fatal("provider was not called")
	}
	if got := string(mock.capturedSpeech.ExtraFields.Lookup("stream_format")); got != `"sse"` {
		t.Errorf("stream_format = %s, want \"sse\"", got)
	}
}
