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
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
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

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls?model=gpt-realtime", strings.NewReader("v=0 offer"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "v=0 answer" {
		t.Errorf("body = %q, want relayed SDP answer", rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/v1/realtime/calls/rtc_abc123" {
		t.Errorf("Location = %q, want gateway-relative call path", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/sdp" {
		t.Errorf("Content-Type = %q, want application/sdp", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff on relayed upstream bytes", got)
	}
	if upstreamReq.body != "v=0 offer" {
		t.Errorf("upstream body = %q, want the SDP offer", upstreamReq.body)
	}
	if upstreamReq.contentType != "application/sdp" {
		t.Errorf("upstream Content-Type = %q, want application/sdp", upstreamReq.contentType)
	}
	if upstreamReq.auth != "Bearer upstream-key" {
		t.Errorf("upstream Authorization = %q, want injected credentials", upstreamReq.auth)
	}
	if upstreamReq.model != "gpt-realtime" {
		t.Errorf("upstream model query = %q, want resolved model", upstreamReq.model)
	}
	if route, ok := handler.realtimeCalls.Lookup("rtc_abc123"); !ok || route.Model != "gpt-realtime" {
		t.Errorf("registry entry = %+v (found %v), want the created call registered", route, ok)
	}
}

func TestRealtimeCalls_MultipartRewritesSessionModel(t *testing.T) {
	var upstreamBody []byte
	var upstreamContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		upstreamContentType = r.Header.Get("Content-Type")
		if r.URL.Query().Has("model") {
			t.Error("model query must be absent when the model travels in the session field")
		}
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

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", &buf)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	// The alias routes the request; the provider must see the resolved model.
	if mock.capturedCall == nil || mock.capturedCall.Model != "gpt-realtime-2" {
		t.Errorf("router received %+v, want the resolved model gpt-realtime-2", mock.capturedCall)
	}

	_, params, err := mime.ParseMediaType(upstreamContentType)
	if err != nil {
		t.Fatalf("upstream content type %q invalid: %v", upstreamContentType, err)
	}
	reader := multipart.NewReader(bytes.NewReader(upstreamBody), params["boundary"])
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("upstream multipart invalid: %v", err)
		}
		content, _ := io.ReadAll(part)
		fields[part.FormName()] = string(content)
	}
	if fields["sdp"] != "v=0 offer" {
		t.Errorf("sdp field = %q, want the original offer", fields["sdp"])
	}
	var session map[string]any
	if err := json.Unmarshal([]byte(fields["session"]), &session); err != nil {
		t.Fatalf("session field is not JSON: %v", err)
	}
	if session["model"] != "gpt-realtime-2" {
		t.Errorf("session.model = %v, want rewritten to the resolved model", session["model"])
	}
	if audio, ok := session["audio"].(map[string]any); !ok || audio["output"] == nil {
		t.Error("session fields beyond model must be preserved")
	}
}

func TestRealtimeCalls_ObserverRecordsUsage(t *testing.T) {
	responseDone := `{"type":"response.done","response":{"usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}}`
	sideband := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("call_id") != "rtc_obs" {
			t.Errorf("sideband call_id = %q, want rtc_obs", r.URL.Query().Get("call_id"))
		}
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

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls?model=gpt-realtime", strings.NewReader("v=0 offer"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedRealtime == nil || mock.capturedRealtime.CallID != "rtc_obs" {
		t.Fatalf("observer target request = %+v, want the created call id", mock.capturedRealtime)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		entries := usageLogger.Entries()
		if len(entries) > 0 {
			entry := entries[0]
			if entry.InputTokens != 12 || entry.OutputTokens != 34 {
				t.Errorf("usage tokens = %d/%d, want 12/34", entry.InputTokens, entry.OutputTokens)
			}
			if entry.Endpoint != "/v1/realtime/calls" {
				t.Errorf("usage endpoint = %q, want /v1/realtime/calls", entry.Endpoint)
			}
			if entry.Model != "gpt-realtime" {
				t.Errorf("usage model = %q, want gpt-realtime", entry.Model)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("observer did not record usage before timeout")
		}
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

			req := httptest.NewRequest(http.MethodPost, tt.target, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/sdp")
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)

			if err := handler.RealtimeCalls(c); err != nil {
				t.Fatalf("RealtimeCalls returned error: %v", err)
			}
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestRealtimeCalls_RouterWithoutCapability(t *testing.T) {
	// A routable provider that lacks the realtime call capability must be
	// rejected up front, not routed.
	handler := NewHandler(&mockProvider{supportedModels: []string{"gpt-realtime"}}, nil, nil, nil)
	handler.realtimeEnabled = true

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls?model=gpt-realtime", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not supported") {
		t.Errorf("body = %q, want a capability error", rec.Body.String())
	}
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

			req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)

			if err := handler.RealtimeCalls(c); err != nil {
				t.Fatalf("RealtimeCalls returned error: %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRealtimeCalls_UnreachableUpstream(t *testing.T) {
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
		callTarget:   &core.RealtimeHTTPTarget{URL: "http://127.0.0.1:1/v1/realtime/calls"},
	}
	handler := newRealtimeTestHandler(mock, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls?model=gpt-realtime", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
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

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls?model=gpt-realtime", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeCalls(c); err != nil {
		t.Fatalf("RealtimeCalls returned error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("Location = %q, want unset when upstream sent none", rec.Header().Get("Location"))
	}
	if mock.capturedRealtime != nil {
		t.Error("no observer must be attached without a call id")
	}
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
	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeClientSecrets(c); err != nil {
		t.Fatalf("RealtimeClientSecrets returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ek_test") {
		t.Errorf("body = %q, want relayed client secret", rec.Body.String())
	}
	if upstreamAuth != "Bearer upstream-key" {
		t.Errorf("upstream Authorization = %q, want injected credentials", upstreamAuth)
	}
	session, _ := upstreamBody["session"].(map[string]any)
	if session == nil || session["model"] != "gpt-realtime-2" {
		t.Errorf("upstream session = %+v, want model rewritten to resolved", upstreamBody["session"])
	}
	if session["instructions"] != "be brief" {
		t.Error("session fields beyond model must be preserved")
	}
	if expires, _ := upstreamBody["expires_after"].(map[string]any); expires == nil {
		t.Error("expires_after must be forwarded")
	}
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
	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeClientSecrets(c); err != nil {
		t.Fatalf("RealtimeClientSecrets returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedSecret == nil || mock.capturedSecret.Model != "gpt-4o-transcribe" {
		t.Errorf("router received %+v, want the transcription model", mock.capturedSecret)
	}
}

func TestRealtimeClientSecrets_InvalidJSON(t *testing.T) {
	mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
	handler := newRealtimeTestHandler(mock, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeClientSecrets(c); err != nil {
		t.Fatalf("RealtimeClientSecrets returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestRealtimeClientSecrets_MissingModel(t *testing.T) {
	mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
	handler := newRealtimeTestHandler(mock, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.RealtimeClientSecrets(c); err != nil {
		t.Fatalf("RealtimeClientSecrets returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestRealtimeAttach_UsesRegistry(t *testing.T) {
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
		// A target with no URL short-circuits proxying with a 502 after the
		// route is resolved, which is all this test needs.
	}
	handler := newRealtimeTestHandler(mock, nil)
	handler.realtimeCalls.Register("rtc_55", realtime.CallRoute{Model: "gpt-realtime", Provider: "mock"})

	req := httptest.NewRequest(http.MethodGet, "/v1/realtime?call_id=rtc_55", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.Realtime(c); err != nil {
		t.Fatalf("Realtime returned error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the empty target (body: %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedRealtime == nil {
		t.Fatal("router was not consulted")
	}
	if mock.capturedRealtime.Model != "gpt-realtime" || mock.capturedRealtime.CallID != "rtc_55" {
		t.Errorf("router received %+v, want registry model and call id", mock.capturedRealtime)
	}
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

	req := httptest.NewRequest(http.MethodGet, "/v1/realtime?call_id=rtc_77&model=other-model", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.Realtime(c); err != nil {
		t.Fatalf("Realtime returned error: %v", err)
	}
	if mock.capturedRealtime == nil {
		t.Fatal("router was not consulted")
	}
	if mock.capturedRealtime.Model != "gpt-realtime" {
		t.Errorf("router received model %q, want the registered call's model", mock.capturedRealtime.Model)
	}
}

func TestRealtimeAttach_UnknownCallID(t *testing.T) {
	mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
	handler := newRealtimeTestHandler(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/realtime?call_id=rtc_missing", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := handler.Realtime(c); err != nil {
		t.Fatalf("Realtime returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
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
		if got := realtimeCallIDFromLocation(tt.location); got != tt.want {
			t.Errorf("realtimeCallIDFromLocation(%q) = %q, want %q", tt.location, got, tt.want)
		}
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
			if err := json.Unmarshal([]byte(tt.payload), &payload); err != nil {
				t.Fatal(err)
			}
			model, setModel := realtimeSessionModel(payload)
			if model != tt.wantModel {
				t.Fatalf("model = %q, want %q", model, tt.wantModel)
			}
			if tt.wantModel == "" {
				return
			}
			setModel("rewritten")
			node := any(payload)
			for _, key := range tt.wantPath {
				node = node.(map[string]any)[key]
			}
			if node != "rewritten" {
				t.Errorf("rewrite landed on %v, want %q at %v", node, "rewritten", tt.wantPath)
			}
		})
	}
}
