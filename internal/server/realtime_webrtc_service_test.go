package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/realtime"
	"github.com/enterpilot/gomodel/internal/usage"
)

// realtimeWebRTCMock extends mockProvider with the realtime routing capabilities
// so the WebRTC signaling handlers can be exercised without a live router.
type realtimeWebRTCMock struct {
	*mockProvider
	resolved       *core.ModelSelector
	callTarget     *core.RealtimeHTTPTarget
	secretTarget   *core.RealtimeHTTPTarget
	realtimeTarget *core.RealtimeTarget

	capturedCall     *core.RealtimeRequest
	capturedSecret   *core.RealtimeRequest
	capturedRealtime *core.RealtimeRequest
}

func (m *realtimeWebRTCMock) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if m.resolved != nil {
		return *m.resolved, true, nil
	}
	selector, err := core.ParseModelSelector(requested.Model, requested.ProviderHint)
	if err != nil {
		return selector, false, err
	}
	if !m.Supports(selector.Model) {
		return selector, false, core.NewNotFoundError("model " + selector.Model + " not found")
	}
	return selector, false, nil
}

func (m *realtimeWebRTCMock) RealtimeCallTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	m.capturedCall = req
	return m.callTarget, nil
}

func (m *realtimeWebRTCMock) RealtimeClientSecretTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	m.capturedSecret = req
	return m.secretTarget, nil
}

func (m *realtimeWebRTCMock) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	m.capturedRealtime = req
	if m.realtimeTarget == nil {
		return &core.RealtimeTarget{}, nil
	}
	return m.realtimeTarget, nil
}

func newRealtimeTestHandler(mock *realtimeWebRTCMock, usageLogger usage.LoggerInterface) *Handler {
	handler := NewHandler(mock, nil, usageLogger, nil)
	handler.realtimeEnabled = true
	return handler
}

func TestRealtimeCalls_SDPHappyPath(t *testing.T) {
	var upstreamReq struct {
		contentType string
		auth        string
		model       string
		body        string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamReq.contentType = r.Header.Get("Content-Type")
		upstreamReq.auth = r.Header.Get("Authorization")
		upstreamReq.model = r.URL.Query().Get("model")
		upstreamReq.body = string(body)
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", "/v1/realtime/calls/rtc_abc123")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0 answer"))
	}))
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
		callTarget: &core.RealtimeHTTPTarget{
			URL:     upstream.URL + "/v1/realtime/calls",
			Headers: http.Header{"Authorization": {"Bearer upstream-key"}},
		},
	}
	handler := newRealtimeTestHandler(mock, nil)

	c, rec := echotest.Post(t, "/v1/realtime/calls?model=gpt-realtime", "v=0 offer", echotest.WithContentType("application/sdp"))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "v=0 answer", rec.Body.String())
	assert.Equal(t, "/v1/realtime/calls/rtc_abc123", rec.Header().Get("Location"))
	assert.Equal(t, "application/sdp", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "v=0 offer", upstreamReq.body)
	assert.Equal(t, "application/sdp", upstreamReq.contentType)
	assert.Equal(t, "Bearer upstream-key", upstreamReq.auth)
	assert.Equal(t, "gpt-realtime", upstreamReq.model)
	route, ok := handler.realtimeCalls.Lookup("rtc_abc123")
	require.True(t, ok, "created call must be registered")
	assert.Equal(t, "gpt-realtime", route.Model)
}

func TestRealtimeCalls_MultipartRewritesSessionModel(t *testing.T) {
	var upstreamBody []byte
	var upstreamContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		upstreamContentType = r.Header.Get("Content-Type")
		assert.False(t, r.URL.Query().Has("model"))

		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0 answer"))
	}))
	defer upstream.Close()

	resolved := core.ModelSelector{Model: "gpt-realtime-2", Provider: "openai"}
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-2"}},
		resolved:     &resolved,
		callTarget:   &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/calls"},
	}
	handler := newRealtimeTestHandler(mock, nil)

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	_ = form.WriteField("sdp", "v=0 offer")
	_ = form.WriteField("session", `{"type":"realtime","model":"voice-alias","audio":{"output":{"voice":"marin"}}}`)
	_ = form.Close()

	c, rec := echotest.Post(t, "/v1/realtime/calls", &buf, echotest.WithContentType(form.FormDataContentType()))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// The alias routes the request; the provider must see the resolved model.
	require.NotNil(t, mock.capturedCall)
	assert.Equal(t, "gpt-realtime-2", mock.capturedCall.Model)

	_, params, err := mime.ParseMediaType(upstreamContentType)
	require.NoError(t, err, "upstream content type %q invalid: %v", upstreamContentType, err)

	reader := multipart.NewReader(bytes.NewReader(upstreamBody), params["boundary"])
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		content, _ := io.ReadAll(part)
		fields[part.FormName()] = string(content)
	}
	assert.Equal(t, "v=0 offer", fields["sdp"])

	var session map[string]any
	err = json.Unmarshal([]byte(fields["session"]), &session)
	require.NoError(t, err)
	assert.Equal(t, "gpt-realtime-2", session["model"])
	audio, ok := session["audio"].(map[string]any)
	require.True(t, ok)
	assert.NotNil(t, audio["output"])
}

func TestRealtimeCalls_ObserverRecordsUsage(t *testing.T) {
	responseDone := `{"type":"response.done","response":{"usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}}`
	sideband := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "rtc_obs", r.URL.Query().Get("call_id"))

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(responseDone))
		conn.Close(websocket.StatusNormalClosure, "call ended")
	}))
	defer sideband.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v1/realtime/calls/rtc_obs")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0 answer"))
	}))
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider:   &mockProvider{supportedModels: []string{"gpt-realtime"}},
		callTarget:     &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/calls"},
		realtimeTarget: &core.RealtimeTarget{URL: "ws" + strings.TrimPrefix(sideband.URL, "http") + "/v1/realtime?call_id=rtc_obs"},
	}
	usageLogger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newRealtimeTestHandler(mock, usageLogger)

	c, rec := echotest.Post(t, "/v1/realtime/calls?model=gpt-realtime", "v=0 offer", echotest.WithContentType("application/sdp"))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotNil(t, mock.capturedRealtime)
	require.Equal(t, "rtc_obs", mock.capturedRealtime.CallID)

	deadline := time.Now().Add(5 * time.Second)
	for {
		entries := usageLogger.Entries()
		if len(entries) > 0 {
			entry := entries[0]
			assert.Equal(t, 12, entry.InputTokens)
			assert.Equal(t, 34, entry.OutputTokens)
			assert.Equal(t, "/v1/realtime/calls", entry.Endpoint)
			assert.Equal(t, "gpt-realtime", entry.Model)

			return
		}
		require.False(t, time.Now().After(deadline))

		time.Sleep(10 * time.Millisecond)
	}
}

func TestRealtimeCalls_ErrorCases(t *testing.T) {
	upstreamError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"bad sdp","type":"invalid_request_error"}}`))
	}))
	defer upstreamError.Close()

	tests := []struct {
		name       string
		enabled    bool
		target     string
		body       string
		wantStatus int
	}{
		{name: "disabled", enabled: false, target: "/v1/realtime/calls?model=gpt-realtime", body: "v=0", wantStatus: http.StatusNotImplemented},
		{name: "missing model", enabled: true, target: "/v1/realtime/calls", body: "v=0", wantStatus: http.StatusBadRequest},
		{name: "empty body", enabled: true, target: "/v1/realtime/calls?model=gpt-realtime", body: "  ", wantStatus: http.StatusBadRequest},
		{name: "unknown model", enabled: true, target: "/v1/realtime/calls?model=nope", body: "v=0", wantStatus: http.StatusNotFound},
		{name: "upstream error relayed", enabled: true, target: "/v1/realtime/calls?model=gpt-realtime", body: "v=0", wantStatus: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &realtimeWebRTCMock{
				mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
				callTarget:   &core.RealtimeHTTPTarget{URL: upstreamError.URL + "/v1/realtime/calls"},
			}
			handler := newRealtimeTestHandler(mock, nil)
			handler.realtimeEnabled = tt.enabled

			c, rec := echotest.Post(t, tt.target, tt.body, echotest.WithContentType("application/sdp"))
			err := handler.RealtimeCalls(c)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
}

func TestRealtimeCalls_RouterWithoutCapability(t *testing.T) {
	// A routable provider that lacks the realtime call capability must be
	// rejected up front, not routed.
	handler := NewHandler(&mockProvider{supportedModels: []string{"gpt-realtime"}}, nil, nil, nil)
	handler.realtimeEnabled = true

	c, rec := echotest.Post(t, "/v1/realtime/calls?model=gpt-realtime", "v=0", echotest.WithContentType("application/sdp"))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "not supported")
}

func TestRealtimeCalls_MalformedMultipart(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "missing boundary", contentType: "multipart/form-data", body: "irrelevant"},
		{
			name:        "invalid session JSON",
			contentType: "multipart/form-data; boundary=b1",
			body:        "--b1\r\nContent-Disposition: form-data; name=\"session\"\r\n\r\nnot-json\r\n--b1--\r\n",
		},
		{
			name:        "truncated multipart body",
			contentType: "multipart/form-data; boundary=b1",
			body:        "--b1\r\nContent-Disposition: form-data",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
			handler := newRealtimeTestHandler(mock, nil)

			c, rec := echotest.Post(t, "/v1/realtime/calls", tt.body, echotest.WithContentType(tt.contentType))
			err := handler.RealtimeCalls(c)
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestRealtimeCalls_UnreachableUpstream(t *testing.T) {
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
		callTarget:   &core.RealtimeHTTPTarget{URL: "http://127.0.0.1:1/v1/realtime/calls"},
	}
	handler := newRealtimeTestHandler(mock, nil)

	c, rec := echotest.Post(t, "/v1/realtime/calls?model=gpt-realtime", "v=0", echotest.WithContentType("application/sdp"))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

func TestRealtimeCalls_NoLocationHeader(t *testing.T) {
	// An upstream that returns no Location header yields no call id: the answer
	// is still relayed, but nothing is registered or observed.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0 answer"))
	}))
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
		callTarget:   &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/calls"},
	}
	usageLogger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newRealtimeTestHandler(mock, usageLogger)

	c, rec := echotest.Post(t, "/v1/realtime/calls?model=gpt-realtime", "v=0", echotest.WithContentType("application/sdp"))
	err := handler.RealtimeCalls(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Header().Get("Location"))
	assert.Nil(t, mock.capturedRealtime)
}

func TestRealtimeClientSecrets_HappyPath(t *testing.T) {
	var upstreamBody map[string]any
	var upstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"realtime.client_secret","value":"ek_test","expires_at":123}`))
	}))
	defer upstream.Close()

	resolved := core.ModelSelector{Model: "gpt-realtime-2", Provider: "openai"}
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-2"}},
		resolved:     &resolved,
		secretTarget: &core.RealtimeHTTPTarget{
			URL:     upstream.URL + "/v1/realtime/client_secrets",
			Headers: http.Header{"Authorization": {"Bearer upstream-key"}},
		},
	}
	handler := newRealtimeTestHandler(mock, nil)

	body := `{"expires_after":{"anchor":"created_at","seconds":600},"session":{"type":"realtime","model":"voice-alias","instructions":"be brief"}}`
	c, rec := echotest.Post(t, "/v1/realtime/client_secrets", body)
	err := handler.RealtimeClientSecrets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "ek_test")
	assert.Equal(t, "Bearer upstream-key", upstreamAuth)

	session, _ := upstreamBody["session"].(map[string]any)
	assert.NotNil(t, session)
	assert.Equal(t, "gpt-realtime-2", session["model"])
	assert.Equal(t, "be brief", session["instructions"])
	expires, _ := upstreamBody["expires_after"].(map[string]any)
	assert.NotNil(t, expires)
}

func TestRealtimeClientSecrets_TranscriptionModelFallback(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		secretTarget: &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/client_secrets"},
	}
	handler := newRealtimeTestHandler(mock, nil)

	body := `{"session":{"type":"transcription","audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}}}`
	c, rec := echotest.Post(t, "/v1/realtime/client_secrets", body)
	err := handler.RealtimeClientSecrets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotNil(t, mock.capturedSecret)
	assert.Equal(t, "gpt-4o-transcribe", mock.capturedSecret.Model)
}

func TestRealtimeClientSecrets_InvalidJSON(t *testing.T) {
	mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
	handler := newRealtimeTestHandler(mock, nil)

	c, rec := echotest.Post(t, "/v1/realtime/client_secrets", "not-json")
	err := handler.RealtimeClientSecrets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestRealtimeClientSecrets_MissingModel(t *testing.T) {
	mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
	handler := newRealtimeTestHandler(mock, nil)

	c, rec := echotest.Post(t, "/v1/realtime/client_secrets", `{"session":{"type":"realtime"}}`)
	err := handler.RealtimeClientSecrets(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestRealtimeAttach_UsesRegistry(t *testing.T) {
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
		// A target with no URL short-circuits proxying with a 502 after the
		// route is resolved, which is all this test needs.
	}
	handler := newRealtimeTestHandler(mock, nil)
	handler.realtimeCalls.Register("rtc_55", realtime.CallRoute{Model: "gpt-realtime", Provider: "mock"})

	c, rec := echotest.Get(t, "/v1/realtime?call_id=rtc_55")
	err := handler.Realtime(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, "empty target must yield 502: %s", rec.Body.String())
	require.NotNil(t, mock.capturedRealtime)
	assert.Equal(t, "gpt-realtime", mock.capturedRealtime.Model)
	assert.Equal(t, "rtc_55", mock.capturedRealtime.CallID)
}

func TestRealtimeAttach_RegistryOverridesConflictingModel(t *testing.T) {
	// The attach dials by call_id alone, so a client naming a different model
	// must not steer model-access or rate-limit checks away from the model the
	// registered call actually runs.
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime", "other-model"}},
	}
	handler := newRealtimeTestHandler(mock, nil)
	handler.realtimeCalls.Register("rtc_77", realtime.CallRoute{Model: "gpt-realtime", Provider: "mock"})

	c, _ := echotest.Get(t, "/v1/realtime?call_id=rtc_77&model=other-model")
	err := handler.Realtime(c)
	require.NoError(t, err)
	require.NotNil(t, mock.capturedRealtime)
	assert.Equal(t, "gpt-realtime", mock.capturedRealtime.Model)
}

func TestRealtimeAttach_UnknownCallID(t *testing.T) {
	mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
	handler := newRealtimeTestHandler(mock, nil)

	c, rec := echotest.Get(t, "/v1/realtime?call_id=rtc_missing")
	err := handler.Realtime(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestRealtimeCallIDFromLocation(t *testing.T) {
	tests := []struct {
		location string
		want     string
	}{
		{location: "/v1/realtime/calls/rtc_123", want: "rtc_123"},
		{location: "https://api.openai.com/v1/realtime/calls/rtc_456", want: "rtc_456"},
		{location: "rtc_789", want: "rtc_789"},
		{location: "", want: ""},
		{location: "   ", want: ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, realtimeCallIDFromLocation(tt.location), "location %q", tt.location)
	}
}

func TestRealtimeSessionModel(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantModel string
		wantPath  []string // where the rewrite must land
	}{
		{
			name:      "realtime session model",
			payload:   `{"session":{"type":"realtime","model":"gpt-realtime"}}`,
			wantModel: "gpt-realtime",
			wantPath:  []string{"session", "model"},
		},
		{
			name:      "transcription nested model",
			payload:   `{"session":{"type":"transcription","audio":{"input":{"transcription":{"model":"whisper-x"}}}}}`,
			wantModel: "whisper-x",
			wantPath:  []string{"session", "audio", "input", "transcription", "model"},
		},
		{name: "no session", payload: `{"other":true}`, wantModel: ""},
		{name: "no model", payload: `{"session":{"type":"realtime"}}`, wantModel: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]any
			err := json.Unmarshal([]byte(tt.payload), &payload)
			require.NoError(t, err)

			model, setModel := realtimeSessionModel(payload)
			require.Equal(t, tt.wantModel, model)

			if tt.wantModel == "" {
				return
			}
			setModel("rewritten")
			node := any(payload)
			for _, key := range tt.wantPath {
				node = node.(map[string]any)[key]
			}
			assert.Equal(t, "rewritten", node, "rewrite must land at %v", tt.wantPath)
		})
	}
}
