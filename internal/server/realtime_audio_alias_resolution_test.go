package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// These tests lock in that virtual-model (alias) resolution reaches the realtime
// and audio endpoints, which resolve their model in the service layer rather than
// through the workflow middleware that covers chat/responses/embeddings.
//
// The regression they guard is subtle: the provider double resolves like the real
// Router — registry-only, so an unknown alias is a 404 and never resolved. Aliases
// only work when a real virtualmodels.Service is wired as the modelResolver and its
// resolved concrete model is forwarded upstream. A prior version wired neither, so
// every alias 404'd on these routes while the (green) unit tests injected a mock
// that faked resolution the Router never performs. Building on the real
// virtualmodels.Service is deliberate: it is the component whose absence caused the
// bug.

// newAliasResolvingService builds a real virtualmodels.Service that redirects
// aliasSource to the concrete openai/<targetModel>.
func newAliasResolvingService(t *testing.T, aliasSource, targetModel string) *virtualmodels.Service {
	t.Helper()
	qualified := "openai/" + targetModel
	catalog := &aliasesTestCatalog{
		supported:     map[string]bool{qualified: true},
		providerTypes: map[string]string{qualified: "openai"},
		models:        map[string]core.Model{qualified: {ID: targetModel, Object: "model"}},
	}
	service, err := virtualmodels.NewService(newAliasesTestStore(
		redirectVM(aliasSource, targetModel, "openai", true),
	), catalog, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service
}

func TestRealtimeClientSecrets_ResolvesAliasThroughVirtualModels(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Session struct {
				Model string `json:"model"`
			} `json:"session"`
		}
		_ = json.Unmarshal(body, &payload)
		upstreamModel = payload.Session.Model
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"value":"ek_test","expires_at":1}`))
	}))
	defer upstream.Close()

	// resolved == nil: the mock resolves registry-only, exactly like the Router,
	// so the alias must be resolved by the wired service before it gets here.
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-2"}},
		secretTarget: &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/client_secrets"},
	}
	service := newAliasResolvingService(t, "voice-alias", "gpt-realtime-2")
	handler := newHandler(mock, nil, nil, nil, service, nil, nil, nil)
	handler.realtimeEnabled = true

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets",
		strings.NewReader(`{"session":{"type":"realtime","model":"voice-alias"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeClientSecrets(c); err != nil {
		t.Fatalf("RealtimeClientSecrets returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (alias should resolve, not 404) (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedSecret == nil || mock.capturedSecret.Model != "gpt-realtime-2" {
		t.Errorf("router received %+v, want the resolved model gpt-realtime-2", mock.capturedSecret)
	}
	if upstreamModel != "gpt-realtime-2" {
		t.Errorf("upstream session.model = %q, want the alias rewritten to gpt-realtime-2", upstreamModel)
	}
}

func TestRealtimeCalls_ResolvesAliasThroughVirtualModels(t *testing.T) {
	var upstreamModelQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		upstreamModelQuery = r.URL.Query().Get("model")
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", "/v1/realtime/calls/rtc_alias1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0 answer"))
	}))
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-2"}},
		callTarget:   &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/calls"},
	}
	service := newAliasResolvingService(t, "voice-alias", "gpt-realtime-2")
	handler := newHandler(mock, nil, nil, nil, service, nil, nil, nil)
	handler.realtimeEnabled = true

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls?model=voice-alias", strings.NewReader("v=0 offer"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (alias should resolve, not 404) (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedCall == nil || mock.capturedCall.Model != "gpt-realtime-2" {
		t.Errorf("router received %+v, want the resolved model gpt-realtime-2", mock.capturedCall)
	}
	if upstreamModelQuery != "gpt-realtime-2" {
		t.Errorf("upstream model query = %q, want the alias rewritten to gpt-realtime-2", upstreamModelQuery)
	}
	// The call is registered under the resolved model so a later sideband attach
	// gates on the concrete model, not the alias.
	if route, ok := handler.realtimeCalls.Lookup("rtc_alias1"); !ok || route.Model != "gpt-realtime-2" {
		t.Errorf("registry entry = %+v (found %v), want the resolved model registered", route, ok)
	}
}

func TestRealtimeWebsocket_ResolvesAliasThroughVirtualModels(t *testing.T) {
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-2"}},
	}
	service := newAliasResolvingService(t, "voice-alias", "gpt-realtime-2")
	handler := newHandler(mock, nil, nil, nil, service, nil, nil, nil)
	handler.realtimeEnabled = true

	// The websocket dial fails (no upstream), but resolution happens before the
	// dial: the captured RealtimeTarget request proves the concrete model — not the
	// alias — reached the router. A 404 here would mean the alias never resolved.
	req := httptest.NewRequest(http.MethodGet, "/v1/realtime?model=voice-alias", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	_ = handler.Realtime(c)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("status = 404, alias failed to resolve (body: %s)", rec.Body.String())
	}
	if mock.capturedRealtime == nil || mock.capturedRealtime.Model != "gpt-realtime-2" {
		t.Errorf("router received %+v, want the resolved model gpt-realtime-2", mock.capturedRealtime)
	}
}

func TestAudioSpeech_ResolvesAliasThroughVirtualModels(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("audio")},
	}
	service := newAliasResolvingService(t, "voice-alias", "gpt-4o-mini-tts")
	handler := newHandler(mock, nil, nil, nil, service, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech",
		strings.NewReader(`{"model":"voice-alias","input":"hello","voice":"alloy"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.AudioSpeech(c); err != nil {
		t.Fatalf("AudioSpeech returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (alias should resolve, not 404) (body: %s)", rec.Code, rec.Body.String())
	}
	// The provider must be dispatched on the resolved model, not the alias.
	if mock.capturedSpeech == nil || mock.capturedSpeech.Model != "gpt-4o-mini-tts" {
		t.Errorf("provider received %+v, want the resolved model gpt-4o-mini-tts", mock.capturedSpeech)
	}
}

func TestAudioTranscription_ResolvesAliasThroughVirtualModels(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider:      &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		transcriptionResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hi"}`)},
	}
	service := newAliasResolvingService(t, "scribe-alias", "gpt-4o-transcribe")
	handler := newHandler(mock, nil, nil, nil, service, nil, nil, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", "scribe-alias")
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
		t.Fatalf("status = %d, want 200 (alias should resolve, not 404) (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedTranscription == nil || mock.capturedTranscription.Model != "gpt-4o-transcribe" {
		t.Errorf("provider received %+v, want the resolved model gpt-4o-transcribe", mock.capturedTranscription)
	}
}
