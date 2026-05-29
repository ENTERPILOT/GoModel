package server

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// audioMockProvider extends mockProvider (a RoutableProvider) with audio support
// so the service layer can be exercised without a live router.
type audioMockProvider struct {
	*mockProvider
	speechResp            *core.AudioResponse
	transcriptionResp     *core.AudioResponse
	audioErr              error
	resolved              *core.ModelSelector
	capturedSpeech        *core.AudioSpeechRequest
	capturedTranscription *core.AudioTranscriptionRequest
}

// ResolveModel lets the fake stand in for the Router so the service can authorize
// on a resolved (provider-qualified) selector. A nil resolved selector falls back
// to the default parse behavior.
func (m *audioMockProvider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if m.resolved != nil {
		return *m.resolved, true, nil
	}
	selector, err := core.ParseModelSelector(requested.Model, requested.ProviderHint)
	return selector, false, err
}

func (m *audioMockProvider) CreateSpeech(_ context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	m.capturedSpeech = req
	if m.audioErr != nil {
		return nil, m.audioErr
	}
	return m.speechResp, nil
}

func (m *audioMockProvider) CreateTranscription(_ context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	m.capturedTranscription = req
	if m.audioErr != nil {
		return nil, m.audioErr
	}
	return m.transcriptionResp, nil
}

func TestAudioSpeech_HappyPath(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")},
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
	if rec.Body.String() != "synthetic-audio" {
		t.Errorf("body = %q, want synthetic-audio", rec.Body.String())
	}
	if mock.capturedSpeech == nil || mock.capturedSpeech.Model != "gpt-4o-mini-tts" || mock.capturedSpeech.Input != "hello" {
		t.Errorf("captured speech request mismatch: %+v", mock.capturedSpeech)
	}
}

func TestAudioSpeech_MissingInput(t *testing.T) {
	mock := &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if mock.capturedSpeech != nil {
		t.Error("provider should not be called when input is missing")
	}
}

func TestAudioSpeech_MissingVoice(t *testing.T) {
	mock := &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if mock.capturedSpeech != nil {
		t.Error("provider should not be called when voice is missing")
	}
}

// TestAudioSpeech_AuthorizesResolvedSelector verifies the authorizer receives the
// registry-resolved selector (provider-qualified), not the raw user-typed model.
func TestAudioSpeech_AuthorizesResolvedSelector(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		resolved:     &core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini-tts"},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("audio")},
	}
	authorizer := &recordingModelAuthorizer{}
	svc := &audioService{provider: mock, modelAuthorizer: authorizer}

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if authorizer.lastSelector.Provider != "openai" {
		t.Errorf("authorizer saw provider %q, want resolved %q", authorizer.lastSelector.Provider, "openai")
	}
	if authorizer.lastSelector.Model != "gpt-4o-mini-tts" {
		t.Errorf("authorizer saw model %q, want %q", authorizer.lastSelector.Model, "gpt-4o-mini-tts")
	}
}

func TestAudioSpeech_AuthorizerDeniesAccess(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		resolved:     &core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini-tts"},
	}
	authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("denied", nil)}
	svc := &audioService{provider: mock, modelAuthorizer: authorizer}

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if mock.capturedSpeech != nil {
		t.Error("provider should not be called when authorization denies access")
	}
}

func TestAudioTranscription_HappyPath(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider:      &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		transcriptionResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hi"}`)},
	}
	handler := NewHandler(mock, nil, nil, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "gpt-4o-transcribe")
	_ = w.WriteField("response_format", "json")
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
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if rec.Body.String() != `{"text":"hi"}` {
		t.Errorf("body = %q", rec.Body.String())
	}
	captured := mock.capturedTranscription
	if captured == nil || captured.Model != "gpt-4o-transcribe" || captured.Filename != "speech.mp3" {
		t.Fatalf("captured transcription request mismatch: %+v", captured)
	}
	if string(captured.File) != "audio-bytes" {
		t.Errorf("captured file = %q, want audio-bytes", string(captured.File))
	}
}

func TestAudioTranscription_MissingModel(t *testing.T) {
	mock := &audioMockProvider{mockProvider: &mockProvider{}}
	handler := NewHandler(mock, nil, nil, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "speech.mp3")
	_, _ = part.Write([]byte("audio-bytes"))
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioTranscriptions(c); err != nil {
		t.Fatalf("AudioTranscriptions returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestAudioSpeech_NilResponseReturns502 covers the respondAudio guard: when the
// provider returns no response and no error, the gateway must report a 502.
func TestAudioSpeech_NilResponseReturns502(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   nil, // provider returns (nil, nil)
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

// TestAudioSpeech_EmptyContentTypeDefaults covers the respondAudio default: an
// empty response content type falls back to application/octet-stream.
func TestAudioSpeech_EmptyContentTypeDefaults(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "", Data: []byte("audio")},
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
}

// TestAudioTranscription_MissingFile covers the multipart guard: a request with a
// model but no file part is rejected with a 400 before any provider call.
func TestAudioTranscription_MissingFile(t *testing.T) {
	mock := &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}}}
	handler := NewHandler(mock, nil, nil, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "gpt-4o-transcribe")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioTranscriptions(c); err != nil {
		t.Fatalf("AudioTranscriptions returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if mock.capturedTranscription != nil {
		t.Error("provider should not be called when file is missing")
	}
}
