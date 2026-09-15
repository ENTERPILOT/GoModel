package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// audioMockProvider extends mockProvider (a RoutableProvider) with audio support
// so the service layer can be exercised without a live router.
type audioMockProvider struct {
	*mockProvider
	speechResp            *core.AudioResponse
	transcriptionResp     *core.AudioResponse
	translationResp       *core.AudioResponse
	audioErr              error
	resolved              *core.ModelSelector
	capturedSpeech        *core.AudioSpeechRequest
	capturedTranscription *core.AudioTranscriptionRequest
	capturedTranslation   *core.AudioTranscriptionRequest
	providerDelay         time.Duration
	providerCalled        chan struct{}
	providerCalledOnce    sync.Once
	providerCompleted     chan struct{}
	providerCompletedOnce sync.Once
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
	m.waitForAudioResponse()
	if m.audioErr != nil {
		return nil, m.audioErr
	}
	return m.speechResp, nil
}

func (m *audioMockProvider) CreateTranscription(_ context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	m.capturedTranscription = req
	m.waitForAudioResponse()
	if m.audioErr != nil {
		return nil, m.audioErr
	}
	return m.transcriptionResp, nil
}

func (m *audioMockProvider) CreateTranslation(_ context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	m.capturedTranslation = req
	m.waitForAudioResponse()
	if m.audioErr != nil {
		return nil, m.audioErr
	}
	return m.translationResp, nil
}

func (m *audioMockProvider) waitForAudioResponse() {
	if m.providerCalled != nil {
		m.providerCalledOnce.Do(func() { close(m.providerCalled) })
	}
	if m.providerDelay > 0 {
		time.Sleep(m.providerDelay)
	}
	if m.providerCompleted != nil {
		m.providerCompletedOnce.Do(func() { close(m.providerCompleted) })
	}
}

type audioSlowdownResolver struct {
	factor    float64
	requested string
	resolved  string
}

func (r *audioSlowdownResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	selector, err := requested.Normalize()
	return selector, false, err
}

func (r *audioSlowdownResolver) ResolveSlowdown(_ context.Context, requested core.RequestedModelSelector, resolved core.ModelSelector) float64 {
	r.requested = requested.RequestedQualifiedModel()
	r.resolved = resolved.QualifiedModel()
	return r.factor
}

func TestAudioSlowdownAppliesAfterInferenceAndHonorsCancellation(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		newContext func(t *testing.T) (*echo.Context, *httptest.ResponseRecorder)
		call       func(*Handler, *echo.Context) error
		response   func(*audioMockProvider)
	}{
		{
			name:  "speech",
			model: "gpt-4o-mini-tts",
			newContext: func(t *testing.T) (*echo.Context, *httptest.ResponseRecorder) {
				c, rec, _ := newSpeechRequestWithAuditEntry(t)
				return c, rec
			},
			call: func(h *Handler, c *echo.Context) error { return h.AudioSpeech(c) },
			response: func(p *audioMockProvider) {
				p.speechResp = &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("audio")}
			},
		},
		{
			name:  "transcription",
			model: "gpt-4o-transcribe",
			newContext: func(t *testing.T) (*echo.Context, *httptest.ResponseRecorder) {
				c, rec, _ := newTranscriptionRequestWithAuditEntry(t, "speech.mp3", []byte("audio"))
				return c, rec
			},
			call: func(h *Handler, c *echo.Context) error { return h.AudioTranscriptions(c) },
			response: func(p *audioMockProvider) {
				p.transcriptionResp = &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hi"}`)}
			},
		},
		{
			name:       "translation",
			model:      "whisper-1",
			newContext: newTranslationSlowdownTestContext,
			call:       func(h *Handler, c *echo.Context) error { return h.AudioTranslations(c) },
			response: func(p *audioMockProvider) {
				p.translationResp = &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hello"}`)}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" success", func(t *testing.T) {
			resolver := &audioSlowdownResolver{factor: 1}
			provider := &audioMockProvider{
				mockProvider:  &mockProvider{supportedModels: []string{tt.model}},
				providerDelay: 15 * time.Millisecond,
			}
			tt.response(provider)
			handler := newHandler(provider, nil, nil, nil, resolver, nil, nil, nil)
			c, rec := tt.newContext(t)

			started := time.Now()
			err := tt.call(handler, c)
			require.NoError(t, err)
			elapsed := time.Since(started)
			require.GreaterOrEqual(t, elapsed, 25*time.Millisecond)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tt.model, resolver.requested)
			require.Equal(t, tt.model, resolver.resolved)
		})

		t.Run(tt.name+" cancellation", func(t *testing.T) {
			resolver := &audioSlowdownResolver{factor: 10}
			provider := &audioMockProvider{
				mockProvider:      &mockProvider{supportedModels: []string{tt.model}},
				providerDelay:     15 * time.Millisecond,
				providerCalled:    make(chan struct{}),
				providerCompleted: make(chan struct{}),
			}
			tt.response(provider)
			handler := newHandler(provider, nil, nil, nil, resolver, nil, nil, nil)
			c, _ := tt.newContext(t)
			ctx, cancel := context.WithCancel(c.Request().Context())
			c.SetRequest(c.Request().WithContext(ctx))

			done := make(chan struct{})
			go func() {
				_ = tt.call(handler, c)
				close(done)
			}()
			select {
			case <-provider.providerCalled:
			case <-time.After(time.Second):
				t.Fatal("provider was not called")
			}
			select {
			case <-provider.providerCompleted:
			case <-time.After(time.Second):
				t.Fatal("provider did not complete")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(150 * time.Millisecond):
				t.Fatal("handler did not cancel the post-inference slowdown")
			}
		})
	}
}

func newTranslationSlowdownTestContext(t *testing.T) (*echo.Context, *httptest.ResponseRecorder) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "whisper-1")
	part, _ := w.CreateFormFile("file", "speech.mp3")
	_, _ = part.Write([]byte("audio"))
	_ = w.Close()
	return echotest.Post(t, "/v1/audio/translations", &buf, echotest.WithContentType(w.FormDataContentType()))
}

func TestAudioSpeech_HappyPath(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")},
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := rec.Header().Get("Content-Type")
	assert.Equal(t, "audio/mpeg", got)
	assert.Equal(t, "synthetic-audio", rec.Body.String())
	require.NotNil(t, mock.capturedSpeech)
	assert.Equal(t, "gpt-4o-mini-tts", mock.capturedSpeech.Model)
	assert.Equal(t, "hello", mock.capturedSpeech.Input)
}

func TestAudioSpeech_MissingInput(t *testing.T) {
	mock := &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","voice":"alloy"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, mock.capturedSpeech)
}

func TestAudioSpeech_MissingVoice(t *testing.T) {
	mock := &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, mock.capturedSpeech)
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
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "openai", authorizer.lastSelector.Provider)
	assert.Equal(t, "gpt-4o-mini-tts", authorizer.lastSelector.Model)
}

func TestAudioSpeech_AuthorizerDeniesAccess(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		resolved:     &core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini-tts"},
	}
	authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("denied", nil)}
	svc := &audioService{provider: mock, modelAuthorizer: authorizer}

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, mock.capturedSpeech)
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
	require.NoError(t, err)

	_, _ = part.Write([]byte("audio-bytes"))
	err = w.Close()
	require.NoError(t, err)

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf, echotest.WithContentType(w.FormDataContentType()))
	err = handler.AudioTranscriptions(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := rec.Header().Get("Content-Type")
	assert.Equal(t, "application/json", got)
	assert.Equal(t, `{"text":"hi"}`, rec.Body.String())

	captured := mock.capturedTranscription
	require.NotNil(t, captured)
	require.Equal(t, "gpt-4o-transcribe", captured.Model)
	require.Equal(t, "speech.mp3", captured.Filename)
	assert.Equal(t, "audio-bytes", string(captured.File))
}

func TestAudioTranscription_UsesConfiguredMultipartMemoryLimit(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "gpt-4o-transcribe")
	part, err := w.CreateFormFile("file", "speech.mp3")
	require.NoError(t, err)

	audio := bytes.Repeat([]byte("a"), 1024)
	_, err = part.Write(audio)
	require.NoError(t, err)
	err = w.Close()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	e := echo.NewWithConfig(echo.Config{FormParseMaxMemory: 1})
	c := e.NewContext(req, rec)

	parsed, err := audioTranscriptionRequestFromForm(c, true)
	require.NoError(t, err)
	require.Equal(t, audio, parsed.File)
	require.NotNil(t, req.MultipartForm)

	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })
	files := req.MultipartForm.File["file"]
	require.Len(t, files, 1)

	upload, err := files[0].Open()
	require.NoError(t, err)

	defer func() { _ = upload.Close() }()
	_, ok := upload.(*os.File)
	require.True(t, ok, "uploaded file type = %T, want disk-backed *os.File", upload)
}

func TestAudioTranslation_HappyPath(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider:    &mockProvider{supportedModels: []string{"whisper-1"}},
		translationResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hello"}`)},
	}
	srv := New(mock, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "whisper-1")
	_ = w.WriteField("prompt", "Use product names")
	_ = w.WriteField("response_format", "json")
	_ = w.WriteField("temperature", "0.2")
	part, err := w.CreateFormFile("file", "speech.wav")
	require.NoError(t, err)

	_, _ = part.Write([]byte("audio-bytes"))
	err = w.Close()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/translations", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, `{"text":"hello"}`, rec.Body.String())

	captured := mock.capturedTranslation
	require.NotNil(t, captured)
	require.Equal(t, "whisper-1", captured.Model)
	require.Equal(t, "speech.wav", captured.Filename)
	assert.Empty(t, captured.Language)
	assert.Empty(t, captured.TimestampGranularities, "translation accepted transcription-only fields: %+v", captured)
	assert.Equal(t, "Use product names", captured.Prompt)
	assert.Equal(t, "json", captured.ResponseFormat)
	assert.Equal(t, "0.2", captured.Temperature, "translation fields were not preserved: %+v", captured)
}

func TestAudioTranslation_ErrorResponses(t *testing.T) {
	tests := []struct {
		name          string
		fields        map[string][]string
		providerError *core.GatewayError
		wantStatus    int
		wantType      core.ErrorType
		wantMessage   string
		wantProvider  bool
	}{
		{
			name:        "rejects language",
			fields:      map[string][]string{"language": {"de"}},
			wantStatus:  http.StatusBadRequest,
			wantType:    core.ErrorTypeInvalidRequest,
			wantMessage: "language is not supported for audio translations",
		},
		{
			name:        "rejects unbracketed timestamp granularities",
			fields:      map[string][]string{"timestamp_granularities": {"word"}},
			wantStatus:  http.StatusBadRequest,
			wantType:    core.ErrorTypeInvalidRequest,
			wantMessage: "timestamp_granularities is not supported for audio translations",
		},
		{
			name:        "rejects bracketed timestamp granularities",
			fields:      map[string][]string{"timestamp_granularities[]": {"word"}},
			wantStatus:  http.StatusBadRequest,
			wantType:    core.ErrorTypeInvalidRequest,
			wantMessage: "timestamp_granularities[] is not supported for audio translations",
		},
		{
			name:          "propagates provider error",
			providerError: core.NewProviderError("openai", http.StatusBadGateway, "translation provider failed", nil),
			wantStatus:    http.StatusBadGateway,
			wantType:      core.ErrorTypeProvider,
			wantMessage:   "translation provider failed",
			wantProvider:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &audioMockProvider{
				mockProvider:    &mockProvider{supportedModels: []string{"whisper-1"}},
				translationResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hello"}`)},
				audioErr:        tt.providerError,
			}
			srv := New(mock, nil)

			var buf bytes.Buffer
			writer := multipart.NewWriter(&buf)
			_ = writer.WriteField("model", "whisper-1")
			for field, values := range tt.fields {
				for _, value := range values {
					_ = writer.WriteField(field, value)
				}
			}
			part, err := writer.CreateFormFile("file", "speech.wav")
			require.NoError(t, err)

			_, _ = part.Write([]byte("audio-bytes"))
			err = writer.Close()
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/v1/audio/translations", &buf)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var envelope core.OpenAIErrorEnvelope
			err = json.Unmarshal(rec.Body.Bytes(), &envelope)
			require.NoError(t, err)
			require.Equal(t, tt.wantType, envelope.Error.Type)
			require.Equal(t, tt.wantMessage, envelope.Error.Message)
			require.Equal(t, tt.wantProvider, mock.capturedTranslation != nil)
		})
	}
}

// newTranscriptionRequestWithAuditEntry builds a multipart /v1/audio/transcriptions
// request carrying the given audio bytes and seeds an empty audit entry.
func newTranscriptionRequestWithAuditEntry(t *testing.T, filename string, audio []byte) (*echo.Context, *httptest.ResponseRecorder, *auditlog.LogEntry) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "gpt-4o-transcribe")
	_ = w.WriteField("language", "en")
	part, _ := w.CreateFormFile("file", filename)
	_, _ = part.Write(audio)
	_ = w.Close()

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf, echotest.WithContentType(w.FormDataContentType()))
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
	c, rec, entry := newTranscriptionRequestWithAuditEntry(t, "speech.mp3", []byte("uploaded-audio-bytes"))
	err := svc.CreateTranscription(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body, ok := entry.Data.RequestBody.(auditlog.AudioBodyLog)
	require.True(t, ok, "request body not an AudioBodyLog, got %T", entry.Data.RequestBody)
	require.True(t, body.Stored)
	require.Equal(t, "audio/mpeg", body.ContentType, "expected stored audio/mpeg (from .mp3 extension), got %+v", body)

	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	assert.NoError(t, err)
	assert.Equal(t, "uploaded-audio-bytes", string(decoded))
	assert.Equal(t, "gpt-4o-transcribe", body.Meta["model"])
	assert.Equal(t, "en", body.Meta["language"], "upload metadata mismatch: %+v", body.Meta)
}

// TestAudioTranscription_MetadataPlaceholderWhenAudioDisabled: with LogBodies on
// but LogAudioBodies off, the upload metadata is still recorded as a placeholder
// (no audio bytes), mirroring the speech-response behavior.
func TestAudioTranscription_MetadataPlaceholderWhenAudioDisabled(t *testing.T) {
	svc := &audioService{provider: newTranscriptionMock(), logBodies: true, logAudioBodies: false}
	c, _, entry := newTranscriptionRequestWithAuditEntry(t, "speech.mp3", []byte("uploaded-audio-bytes"))
	err := svc.CreateTranscription(c)
	require.NoError(t, err)

	body, ok := entry.Data.RequestBody.(auditlog.AudioBodyLog)
	require.True(t, ok, "request body not an AudioBodyLog, got %T", entry.Data.RequestBody)
	assert.False(t, body.Stored)
	assert.Empty(t, body.Data, "audio bytes must not be stored when LogAudioBodies is off, got %+v", body)
	assert.Equal(t, "gpt-4o-transcribe", body.Meta["model"], "metadata must be preserved on the placeholder, got %+v", body.Meta)
}

// TestAudioTranscription_NoCaptureWhenBodiesDisabled: nothing is captured when
// the master LogBodies switch is off.
func TestAudioTranscription_NoCaptureWhenBodiesDisabled(t *testing.T) {
	svc := &audioService{provider: newTranscriptionMock(), logBodies: false, logAudioBodies: true}
	c, _, entry := newTranscriptionRequestWithAuditEntry(t, "speech.mp3", []byte("uploaded-audio-bytes"))
	err := svc.CreateTranscription(c)
	require.NoError(t, err)

	if entry.Data != nil {
		assert.Nil(t, entry.Data.RequestBody, "nothing should be captured when LogBodies is off")
	}
}

// TestAudioSpeech_LogsUsage verifies a text-to-speech call records a usage entry
// keyed by the input character count when usage tracking is enabled.
func TestAudioSpeech_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{provider: newSpeechMock(), usageLogger: logger}
	c, rec, _ := newSpeechRequestWithAuditEntry(t)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, captured)
	assert.Equal(t, "/v1/audio/speech", captured.Endpoint)
	assert.Equal(t, "gpt-4o-mini-tts", captured.Model)
	got := captured.RawData["input_characters"]
	assert.Equal(t, len("hello"), got)
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
			c, _ := echotest.Post(t, "/v1/audio/speech", body, echotest.WithValue(string(auditlog.LogEntryKey), &auditlog.LogEntry{}))
			err := svc.CreateSpeech(c)
			require.NoError(t, err)
			require.NotNil(t, captured)

			got, hasSeconds := captured.RawData["audio_output_seconds"]
			if tt.wantSeconds > 0 {
				assert.True(t, hasSeconds)
				assert.Equal(t, tt.wantSeconds, got)
			} else {
				assert.False(t, hasSeconds, "unexpected audio_output_seconds %v", got)
			}

			require.NotNil(t, captured.TotalCost)
			require.Equal(t, tt.wantCost, *captured.TotalCost)
			hasCaveat := captured.CostsCalculationCaveat != ""
			assert.Equal(t, tt.wantCaveat, hasCaveat, "caveat %q", captured.CostsCalculationCaveat)
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
	c, rec, _ := newTranscriptionRequestWithAuditEntry(t, "speech.mp3", []byte("audio-bytes"))
	err := svc.CreateTranscription(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, captured)
	assert.Equal(t, "/v1/audio/transcriptions", captured.Endpoint)
	assert.Equal(t, "gpt-4o-transcribe", captured.Model)
}

// TestAudioSpeech_NoUsageWhenDisabled verifies nothing is written when usage
// tracking is off.
func TestAudioSpeech_NoUsageWhenDisabled(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: false}, captured: &captured}
	svc := &audioService{provider: newSpeechMock(), usageLogger: logger}
	c, rec, _ := newSpeechRequestWithAuditEntry(t)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, captured)
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
		assert.Equal(t, tc.want, got, "audioUploadContentType(ct=%q, file=%q)", tc.contentType, tc.filename)
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

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf, echotest.WithContentType(w.FormDataContentType()))
	err := handler.AudioTranscriptions(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// newSpeechRequestWithAuditEntry builds a /v1/audio/speech request and seeds an
// empty audit entry into the context (as the audit middleware would), returning
// the context, recorder, and the entry to assert on.
func newSpeechRequestWithAuditEntry(t *testing.T) (*echo.Context, *httptest.ResponseRecorder, *auditlog.LogEntry) {
	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
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
	c, rec, entry := newSpeechRequestWithAuditEntry(t)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	reqBody, ok := entry.Data.RequestBody.(map[string]any)
	require.True(t, ok, "request body not captured as map, got %T", entry.Data.RequestBody)
	assert.Equal(t, "hello", reqBody["input"])
	assert.Equal(t, "alloy", reqBody["voice"], "request body mismatch: %+v", reqBody)

	respBody, ok := entry.Data.ResponseBody.(auditlog.AudioBodyLog)
	require.True(t, ok, "response body not an AudioBodyLog, got %T", entry.Data.ResponseBody)
	require.True(t, respBody.Stored)
	require.Equal(t, "base64", respBody.Encoding, "expected stored base64 audio, got %+v", respBody)

	decoded, err := base64.StdEncoding.DecodeString(respBody.Data)
	assert.NoError(t, err)
	assert.Equal(t, "synthetic-audio", string(decoded))
}

// TestAudioSpeech_PlaceholderWhenAudioDisabled: with LogBodies on but
// LogAudioBodies off, the audio response is a metadata-only placeholder and the
// input is not captured.
func TestAudioSpeech_PlaceholderWhenAudioDisabled(t *testing.T) {
	svc := &audioService{provider: newSpeechMock(), logBodies: true, logAudioBodies: false}
	c, _, entry := newSpeechRequestWithAuditEntry(t)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)

	if entry.Data != nil {
		assert.Nil(t, entry.Data.RequestBody, "input should not be captured when LogAudioBodies is off")
	}
	respBody, ok := entry.Data.ResponseBody.(auditlog.AudioBodyLog)
	require.True(t, ok, "response body not an AudioBodyLog, got %T", entry.Data.ResponseBody)
	assert.False(t, respBody.Stored)
	assert.Empty(t, respBody.Data, "audio bytes must not be stored when LogAudioBodies is off, got %+v", respBody)
	assert.Equal(t, len("synthetic-audio"), respBody.Bytes)
}

// TestAudioSpeech_NoAudioBodyWhenBodiesDisabled: LogBodies is the master switch.
// With it off, no audio body is captured even if LogAudioBodies is on.
func TestAudioSpeech_NoAudioBodyWhenBodiesDisabled(t *testing.T) {
	svc := &audioService{provider: newSpeechMock(), logBodies: false, logAudioBodies: true}
	c, rec, entry := newSpeechRequestWithAuditEntry(t)
	err := svc.CreateSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	if entry.Data != nil {
		assert.Nil(t, entry.Data.RequestBody, "no request body should be captured when LogBodies is off")
		assert.Nil(t, entry.Data.ResponseBody, "no response body should be captured when LogBodies is off")
	}
}

// TestAudioSpeech_NilResponseReturns502 covers the respondAudio guard: when the
// provider returns no response and no error, the gateway must report a 502.
func TestAudioSpeech_NilResponseReturns502(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{
			supportedModels: []string{"gpt-4o-mini-tts"},
			providerNames:   map[string]string{"gpt-4o-mini-tts": "audio-primary"},
		},
		speechResp: nil, // provider returns (nil, nil)
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), `"provider":"audio-primary"`)
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
			usageLogger: logger}
		c, rec, _ := newSpeechRequestWithAuditEntry(t)
		err := svc.CreateSpeech(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Nil(t, captured)
	})

	t.Run("transcription", func(t *testing.T) {
		var captured *usage.UsageEntry
		logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
		svc := &audioService{
			provider: &audioMockProvider{mockProvider: &mockProvider{
				supportedModels: []string{"gpt-4o-transcribe"},
				providerNames:   map[string]string{"gpt-4o-transcribe": "audio-transcription"},
			}, transcriptionResp: nil},
			usageLogger: logger}
		c, rec, _ := newTranscriptionRequestWithAuditEntry(t, "speech.mp3", []byte("audio-bytes"))
		err := // Must not panic on resp.Data when resp is nil.
			svc.CreateTranscription(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Contains(t, rec.Body.String(), `"provider":"audio-transcription"`)
		assert.Contains(t, rec.Body.String(), "provider audio-transcription returned empty audio response")
		assert.Nil(t, captured)
	})

	t.Run("translation", func(t *testing.T) {
		var captured *usage.UsageEntry
		logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
		svc := &audioService{
			provider: &audioMockProvider{mockProvider: &mockProvider{
				supportedModels: []string{"gpt-4o-translate"},
				providerNames:   map[string]string{"gpt-4o-translate": "audio-translation"},
			}, translationResp: nil},
			usageLogger: logger,
		}

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("model", "gpt-4o-translate")
		part, _ := writer.CreateFormFile("file", "speech.mp3")
		_, _ = part.Write([]byte("audio-bytes"))
		_ = writer.Close()
		c, rec := echotest.Post(t, "/v1/audio/translations", &body, echotest.WithContentType(writer.FormDataContentType()))
		err := svc.CreateTranslation(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Contains(t, rec.Body.String(), `"provider":"audio-translation"`)
		assert.Contains(t, rec.Body.String(), "provider audio-translation returned empty audio response")
		assert.Nil(t, captured)
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
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	got := rec.Header().Get("Content-Type")
	assert.Equal(t, "application/octet-stream", got)
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

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf, echotest.WithContentType(w.FormDataContentType()))
	err := handler.AudioTranscriptions(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, mock.capturedTranscription)
}
