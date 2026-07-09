package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/usage"
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

// newTranscriptionRequestWithAuditEntry builds a multipart /v1/audio/transcriptions
// request carrying the given audio bytes and seeds an empty audit entry.
func newTranscriptionRequestWithAuditEntry(filename string, audio []byte) (*echo.Context, *httptest.ResponseRecorder, *auditlog.LogEntry) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "gpt-4o-transcribe")
	_ = w.WriteField("language", "en")
	part, _ := w.CreateFormFile("file", filename)
	_, _ = part.Write(audio)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)
	return c, rec, entry
}

func newTranscriptionMock() *audioMockProvider {
	return &audioMockProvider{
		mockProvider:      &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		transcriptionResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hi"}`)},
	}
}

// TestAudioTranscription_LogsUploadedAudioWhenEnabled: with both flags on, the
// uploaded audio is captured losslessly as a playable base64 request body, with
// the upload metadata attached. Content type falls back to the filename
// extension when the multipart part declares a non-audio type.
func TestAudioTranscription_LogsUploadedAudioWhenEnabled(t *testing.T) {
	svc := &audioService{provider: newTranscriptionMock(), logBodies: true, logAudioBodies: true}
	c, rec, entry := newTranscriptionRequestWithAuditEntry("speech.mp3", []byte("uploaded-audio-bytes"))

	if err := svc.CreateTranscription(c); err != nil {
		t.Fatalf("CreateTranscription returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	body, ok := entry.Data.RequestBody.(auditlog.AudioBodyLog)
	if !ok {
		t.Fatalf("request body not an AudioBodyLog, got %T", entry.Data.RequestBody)
	}
	if !body.Stored || body.ContentType != "audio/mpeg" {
		t.Fatalf("expected stored audio/mpeg (from .mp3 extension), got %+v", body)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || string(decoded) != "uploaded-audio-bytes" {
		t.Errorf("uploaded audio not preserved losslessly: decoded=%q err=%v", decoded, err)
	}
	if body.Meta["model"] != "gpt-4o-transcribe" || body.Meta["language"] != "en" {
		t.Errorf("upload metadata mismatch: %+v", body.Meta)
	}
}

// TestAudioTranscription_MetadataPlaceholderWhenAudioDisabled: with LogBodies on
// but LogAudioBodies off, the upload metadata is still recorded as a placeholder
// (no audio bytes), mirroring the speech-response behavior.
func TestAudioTranscription_MetadataPlaceholderWhenAudioDisabled(t *testing.T) {
	svc := &audioService{provider: newTranscriptionMock(), logBodies: true, logAudioBodies: false}
	c, _, entry := newTranscriptionRequestWithAuditEntry("speech.mp3", []byte("uploaded-audio-bytes"))

	if err := svc.CreateTranscription(c); err != nil {
		t.Fatalf("CreateTranscription returned error: %v", err)
	}
	body, ok := entry.Data.RequestBody.(auditlog.AudioBodyLog)
	if !ok {
		t.Fatalf("request body not an AudioBodyLog, got %T", entry.Data.RequestBody)
	}
	if body.Stored || body.Data != "" {
		t.Errorf("audio bytes must not be stored when LogAudioBodies is off, got %+v", body)
	}
	if body.Meta["model"] != "gpt-4o-transcribe" {
		t.Errorf("metadata must be preserved on the placeholder, got %+v", body.Meta)
	}
}

// TestAudioTranscription_NoCaptureWhenBodiesDisabled: nothing is captured when
// the master LogBodies switch is off.
func TestAudioTranscription_NoCaptureWhenBodiesDisabled(t *testing.T) {
	svc := &audioService{provider: newTranscriptionMock(), logBodies: false, logAudioBodies: true}
	c, _, entry := newTranscriptionRequestWithAuditEntry("speech.mp3", []byte("uploaded-audio-bytes"))

	if err := svc.CreateTranscription(c); err != nil {
		t.Fatalf("CreateTranscription returned error: %v", err)
	}
	if entry.Data != nil && entry.Data.RequestBody != nil {
		t.Errorf("nothing should be captured when LogBodies is off, got %+v", entry.Data.RequestBody)
	}
}

// TestAudioSpeech_LogsUsage verifies a text-to-speech call records a usage entry
// keyed by the input character count when usage tracking is enabled.
func TestAudioSpeech_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{provider: newSpeechMock(), usageLogger: logger}
	c, rec, _ := newSpeechRequestWithAuditEntry()

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("expected a usage entry to be written")
	}
	if captured.Endpoint != "/v1/audio/speech" {
		t.Errorf("endpoint = %q, want /v1/audio/speech", captured.Endpoint)
	}
	if captured.Model != "gpt-4o-mini-tts" {
		t.Errorf("model = %q, want gpt-4o-mini-tts", captured.Model)
	}
	if got := captured.RawData["input_characters"]; got != len("hello") {
		t.Errorf("input_characters = %v, want %d", got, len("hello"))
	}
}

// TestAudioSpeech_CostsOutputAudioDuration verifies the full wire path for
// output-duration-priced TTS models (e.g. gpt-4o-mini-tts), including how the
// billing format is resolved: the response Content-Type is authoritative, the
// requested response_format is the fallback, and mp3 is the final default.
func TestAudioSpeech_CostsOutputAudioDuration(t *testing.T) {
	// 2-second 24 kHz mono 16-bit WAV: byteRate 48000, dataLen 96000.
	wav := wavBytes(24000, 1, 16, 2.0)
	mp3 := []byte("\xff\xfbnot-a-wav-body")
	// gpt-4o-mini-tts-style pricing: tiny text input plus per-second audio output.
	pricing := &core.ModelPricing{InputPerMtok: new(0.6), PerSecondOutput: new(0.00025)}

	tests := []struct {
		name           string
		responseFormat string // omitted from the request body when empty
		contentType    string
		data           []byte
		wantSeconds    float64 // 0 => no measured duration expected
		wantCost       float64
		wantCaveat     bool
	}{
		{"explicit wav", "wav", "audio/wav", wav, 2, 0.0005, false},
		{"content-type fallback wav", "", "audio/wav", wav, 2, 0.0005, false},
		// The client requested a measurable format (pcm) but the provider
		// actually returned mp3; the response Content-Type must win so the
		// non-PCM bytes are caveated, not charged as len/48000 of fake PCM.
		{"content-type overrides requested format", "pcm", "audio/mpeg", mp3, 0, 0, true},
		{"default mp3 unmeasured", "", "", mp3, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured *usage.UsageEntry
			logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
			mock := &audioMockProvider{
				mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
				speechResp:   &core.AudioResponse{ContentType: tt.contentType, Data: tt.data},
			}
			svc := &audioService{provider: mock, usageLogger: logger, pricingResolver: &mockPricingResolver{pricing: pricing}}

			body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"`
			if tt.responseFormat != "" {
				body += `,"response_format":"` + tt.responseFormat + `"`
			}
			body += `}`
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			c := echo.New().NewContext(req, httptest.NewRecorder())
			c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{})

			if err := svc.CreateSpeech(c); err != nil {
				t.Fatalf("CreateSpeech returned error: %v", err)
			}
			if captured == nil {
				t.Fatal("expected a usage entry to be written")
			}

			got, hasSeconds := captured.RawData["audio_output_seconds"]
			if tt.wantSeconds > 0 {
				if !hasSeconds || got != tt.wantSeconds {
					t.Errorf("audio_output_seconds = %v (present=%v), want %v", got, hasSeconds, tt.wantSeconds)
				}
			} else if hasSeconds {
				t.Errorf("audio_output_seconds = %v, want none", got)
			}

			if captured.TotalCost == nil || *captured.TotalCost != tt.wantCost {
				t.Fatalf("total_cost = %v, want %v", captured.TotalCost, tt.wantCost)
			}
			if hasCaveat := captured.CostsCalculationCaveat != ""; hasCaveat != tt.wantCaveat {
				t.Errorf("caveat = %q, want present=%v", captured.CostsCalculationCaveat, tt.wantCaveat)
			}
		})
	}
}

// wavBytes builds a minimal canonical PCM WAV of the requested duration.
func wavBytes(sampleRate, channels, bitsPerSample int, seconds float64) []byte {
	byteRate := sampleRate * channels * bitsPerSample / 8
	dataLen := int(float64(byteRate) * seconds)
	le := func(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }
	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }
	out := []byte("RIFF")
	out = append(out, le(uint32(36+dataLen))...)
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = append(out, le(16)...)
	out = append(out, le16(1)...)
	out = append(out, le16(uint16(channels))...)
	out = append(out, le(uint32(sampleRate))...)
	out = append(out, le(uint32(byteRate))...)
	out = append(out, le16(uint16(channels*bitsPerSample/8))...)
	out = append(out, le16(uint16(bitsPerSample))...)
	out = append(out, "data"...)
	out = append(out, le(uint32(dataLen))...)
	out = append(out, make([]byte, dataLen)...)
	return out
}

// TestAudioTranscription_LogsUsage verifies a speech-to-text call records a usage
// entry even when the provider response carries no usage object (e.g. whisper).
func TestAudioTranscription_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{provider: newTranscriptionMock(), usageLogger: logger}
	c, rec, _ := newTranscriptionRequestWithAuditEntry("speech.mp3", []byte("audio-bytes"))

	if err := svc.CreateTranscription(c); err != nil {
		t.Fatalf("CreateTranscription returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("expected a usage entry to be written")
	}
	if captured.Endpoint != "/v1/audio/transcriptions" {
		t.Errorf("endpoint = %q, want /v1/audio/transcriptions", captured.Endpoint)
	}
	if captured.Model != "gpt-4o-transcribe" {
		t.Errorf("model = %q, want gpt-4o-transcribe", captured.Model)
	}
}

// TestAudioSpeech_NoUsageWhenDisabled verifies nothing is written when usage
// tracking is off.
func TestAudioSpeech_NoUsageWhenDisabled(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: false}, captured: &captured}
	svc := &audioService{provider: newSpeechMock(), usageLogger: logger}
	c, rec, _ := newSpeechRequestWithAuditEntry()

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if captured != nil {
		t.Errorf("no usage entry should be written when disabled, got %+v", captured)
	}
}

func TestAudioUploadContentType(t *testing.T) {
	cases := []struct {
		contentType string
		filename    string
		want        string
	}{
		{"audio/wav; codecs=1", "x.bin", "audio/wav"},
		{"audio/webm; codecs=opus", "x", "audio/webm"},
		{"AUDIO/MPEG", "x", "audio/mpeg"},
		{"application/octet-stream", "speech.mp3", "audio/mpeg"},
		{"", "clip.wav", "audio/wav"},
		{"", "clip.ogg", "audio/ogg"},
		{"", "clip.flac", "audio/flac"},
		{"", "clip.m4a", "audio/mp4"},
		{"", "unknown", "audio/mpeg"},
	}
	for _, tc := range cases {
		got := audioUploadContentType(&core.AudioTranscriptionRequest{FileContentType: tc.contentType, Filename: tc.filename})
		if got != tc.want {
			t.Errorf("audioUploadContentType(ct=%q, file=%q) = %q, want %q", tc.contentType, tc.filename, got, tc.want)
		}
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

// newSpeechRequestWithAuditEntry builds a /v1/audio/speech request and seeds an
// empty audit entry into the context (as the audit middleware would), returning
// the context, recorder, and the entry to assert on.
func newSpeechRequestWithAuditEntry() (*echo.Context, *httptest.ResponseRecorder, *auditlog.LogEntry) {
	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)
	return c, rec, entry
}

func newSpeechMock() *audioMockProvider {
	return &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")},
	}
}

// TestAudioSpeech_LogsAudioBodiesWhenEnabled: with both LogBodies and
// LogAudioBodies on, the speech input is logged and the audio output is stored
// losslessly as base64 for playback.
func TestAudioSpeech_LogsAudioBodiesWhenEnabled(t *testing.T) {
	svc := &audioService{provider: newSpeechMock(), logBodies: true, logAudioBodies: true}
	c, rec, entry := newSpeechRequestWithAuditEntry()

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	reqBody, ok := entry.Data.RequestBody.(map[string]any)
	if !ok {
		t.Fatalf("request body not captured as map, got %T", entry.Data.RequestBody)
	}
	if reqBody["input"] != "hello" || reqBody["voice"] != "alloy" {
		t.Errorf("request body mismatch: %+v", reqBody)
	}

	respBody, ok := entry.Data.ResponseBody.(auditlog.AudioBodyLog)
	if !ok {
		t.Fatalf("response body not an AudioBodyLog, got %T", entry.Data.ResponseBody)
	}
	if !respBody.Stored || respBody.Encoding != "base64" {
		t.Fatalf("expected stored base64 audio, got %+v", respBody)
	}
	decoded, err := base64.StdEncoding.DecodeString(respBody.Data)
	if err != nil || string(decoded) != "synthetic-audio" {
		t.Errorf("base64 did not round-trip to the audio bytes: decoded=%q err=%v", decoded, err)
	}
}

// TestAudioSpeech_PlaceholderWhenAudioDisabled: with LogBodies on but
// LogAudioBodies off, the audio response is a metadata-only placeholder and the
// input is not captured.
func TestAudioSpeech_PlaceholderWhenAudioDisabled(t *testing.T) {
	svc := &audioService{provider: newSpeechMock(), logBodies: true, logAudioBodies: false}
	c, _, entry := newSpeechRequestWithAuditEntry()

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}

	if entry.Data != nil && entry.Data.RequestBody != nil {
		t.Errorf("input should not be captured when LogAudioBodies is off, got %+v", entry.Data.RequestBody)
	}
	respBody, ok := entry.Data.ResponseBody.(auditlog.AudioBodyLog)
	if !ok {
		t.Fatalf("response body not an AudioBodyLog, got %T", entry.Data.ResponseBody)
	}
	if respBody.Stored || respBody.Data != "" {
		t.Errorf("audio bytes must not be stored when LogAudioBodies is off, got %+v", respBody)
	}
	if respBody.Bytes != len("synthetic-audio") {
		t.Errorf("placeholder should still record byte size, got %d", respBody.Bytes)
	}
}

// TestAudioSpeech_NoAudioBodyWhenBodiesDisabled: LogBodies is the master switch.
// With it off, no audio body is captured even if LogAudioBodies is on.
func TestAudioSpeech_NoAudioBodyWhenBodiesDisabled(t *testing.T) {
	svc := &audioService{provider: newSpeechMock(), logBodies: false, logAudioBodies: true}
	c, rec, entry := newSpeechRequestWithAuditEntry()

	if err := svc.CreateSpeech(c); err != nil {
		t.Fatalf("CreateSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if entry.Data != nil && (entry.Data.RequestBody != nil || entry.Data.ResponseBody != nil) {
		t.Errorf("no body should be captured when LogBodies is off, got req=%+v resp=%+v",
			entry.Data.RequestBody, entry.Data.ResponseBody)
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

// TestAudio_NilResponseSkipsUsage covers the nil-response guard: a (nil, nil)
// provider result must return 502 without writing a usage row — and for
// transcription, without dereferencing resp.Data (which would panic).
func TestAudio_NilResponseSkipsUsage(t *testing.T) {
	t.Run("speech", func(t *testing.T) {
		var captured *usage.UsageEntry
		logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
		svc := &audioService{
			provider:    &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}, speechResp: nil},
			usageLogger: logger,
		}
		c, rec, _ := newSpeechRequestWithAuditEntry()

		if err := svc.CreateSpeech(c); err != nil {
			t.Fatalf("CreateSpeech returned error: %v", err)
		}
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if captured != nil {
			t.Errorf("no usage should be written for a failed call, got %+v", captured)
		}
	})

	t.Run("transcription", func(t *testing.T) {
		var captured *usage.UsageEntry
		logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
		svc := &audioService{
			provider:    &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}}, transcriptionResp: nil},
			usageLogger: logger,
		}
		c, rec, _ := newTranscriptionRequestWithAuditEntry("speech.mp3", []byte("audio-bytes"))

		// Must not panic on resp.Data when resp is nil.
		if err := svc.CreateTranscription(c); err != nil {
			t.Fatalf("CreateTranscription returned error: %v", err)
		}
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if captured != nil {
			t.Errorf("no usage should be written for a failed call, got %+v", captured)
		}
	})
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
