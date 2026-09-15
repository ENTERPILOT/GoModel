package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

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

	c, rec := echotest.Post(t, "/v1/realtime/client_secrets", `{"session":{"type":"realtime","model":"voice-alias"}}`)
	err := handler.RealtimeClientSecrets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, "alias should resolve, not 404: %s", rec.Body.String())
	require.NotNil(t, mock.capturedSecret)
	assert.Equal(t, "gpt-realtime-2", mock.capturedSecret.Model)
	assert.Equal(t, "gpt-realtime-2", upstreamModel)
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

	c, rec := echotest.Post(t, "/v1/realtime/calls?model=voice-alias", "v=0 offer", echotest.WithContentType("application/sdp"))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code, "alias should resolve, not 404: %s", rec.Body.String())
	require.NotNil(t, mock.capturedCall)
	assert.Equal(t, "gpt-realtime-2", mock.capturedCall.Model)
	assert.Equal(t, "gpt-realtime-2", upstreamModelQuery)
	// The call is registered under the resolved model so a later sideband attach
	// gates on the concrete model, not the alias.
	route, ok := handler.realtimeCalls.Lookup("rtc_alias1")
	require.True(t, ok)
	assert.Equal(t, "gpt-realtime-2", route.Model)
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
	c, rec := echotest.Get(t, "/v1/realtime?model=voice-alias")

	_ = handler.Realtime(c)

	require.NotEqual(t, http.StatusNotFound, rec.Code, "status = 404, alias failed to resolve (body: %s)", rec.Body.String())
	require.NotNil(t, mock.capturedRealtime)
	assert.Equal(t, "gpt-realtime-2", mock.capturedRealtime.Model)
}

func TestAudioSpeech_ResolvesAliasThroughVirtualModels(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("audio")},
	}
	service := newAliasResolvingService(t, "voice-alias", "gpt-4o-mini-tts")
	handler := newHandler(mock, nil, nil, nil, service, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/audio/speech", `{"model":"voice-alias","input":"hello","voice":"alloy"}`)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, "alias should resolve, not 404: %s", rec.Body.String())

	// The provider must be dispatched on the resolved model, not the alias.
	require.NotNil(t, mock.capturedSpeech)
	assert.Equal(t, "gpt-4o-mini-tts", mock.capturedSpeech.Model)
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
	require.NoError(t, err)

	_, _ = part.Write([]byte("audio-bytes"))
	err = w.Close()
	require.NoError(t, err)

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf, echotest.WithContentType(w.FormDataContentType()))
	err = handler.AudioTranscriptions(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, "alias should resolve, not 404: %s", rec.Body.String())
	require.NotNil(t, mock.capturedTranscription)
	assert.Equal(t, "gpt-4o-transcribe", mock.capturedTranscription.Model)
}
