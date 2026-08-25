package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
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

func TestRealtimeTranslationsRouteSetsIntent(t *testing.T) {
	// The dedicated route fixes the intent, so a client that targets the
	// provider's translation surface reaches it without a query parameter. The
	// websocket dial fails (no upstream), but the request is captured first.
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-translate"}},
	}
	handler := newRealtimeTestHandler(mock, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/realtime/translations?model=gpt-realtime-translate", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	_ = handler.RealtimeTranslations(c)

	if mock.capturedRealtime == nil {
		t.Fatalf("router received no request (status %d, body %s)", rec.Code, rec.Body.String())
	}
	if mock.capturedRealtime.Intent != core.RealtimeIntentTranslation {
		t.Errorf("intent = %q, want %q", mock.capturedRealtime.Intent, core.RealtimeIntentTranslation)
	}
	if mock.capturedRealtime.Model != "gpt-realtime-translate" {
		t.Errorf("model = %q, want the resolved model", mock.capturedRealtime.Model)
	}
}

func TestRealtimeTranslationSignalingRoutesSetIntent(t *testing.T) {
	// WebRTC calls and client secrets have translation siblings too: the intent
	// is what steers the provider to the translation surface.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v1/realtime/translations/calls/rtc_x")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("v=0 answer"))
	}))
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-translate"}},
		callTarget:   &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/translations/calls"},
		secretTarget: &core.RealtimeHTTPTarget{URL: upstream.URL + "/v1/realtime/translations/client_secrets"},
	}
	handler := newRealtimeTestHandler(mock, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/realtime/translations/calls?model=gpt-realtime-translate", strings.NewReader("v=0 offer"))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	if err := handler.RealtimeTranslationCalls(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("RealtimeTranslationCalls returned error: %v", err)
	}
	if mock.capturedCall == nil || mock.capturedCall.Intent != core.RealtimeIntentTranslation {
		t.Errorf("call request = %+v, want the translation intent", mock.capturedCall)
	}
	// A translation call lives under its own path, so the relayed Location must
	// address it there.
	if got := rec.Header().Get("Location"); got != "/v1/realtime/translations/calls/rtc_x" {
		t.Errorf("Location = %q, want the translation calls path", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/realtime/translations/client_secrets",
		strings.NewReader(`{"session":{"model":"gpt-realtime-translate","audio":{"output":{"language":"es"}}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	if err := handler.RealtimeTranslationClientSecrets(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("RealtimeTranslationClientSecrets returned error: %v", err)
	}
	if mock.capturedSecret == nil || mock.capturedSecret.Intent != core.RealtimeIntentTranslation {
		t.Errorf("client secret request = %+v, want the translation intent", mock.capturedSecret)
	}
}

func TestPassthroughRealtimeTranslationsSetsIntent(t *testing.T) {
	// The passthrough surface mirrors the typed one: an upgrade on
	// /p/{provider}/v1/realtime/translations opens a translation session, while
	// any other realtime sub-path stays unsupported.
	tests := []struct {
		name       string
		path       string
		wantIntent string
		wantRouted bool
	}{
		{name: "conversation", path: "/p/openai/v1/realtime?model=gpt-realtime", wantRouted: true},
		{name: "translations", path: "/p/openai/v1/realtime/translations?model=gpt-realtime", wantIntent: core.RealtimeIntentTranslation, wantRouted: true},
		{name: "unknown sub-path", path: "/p/openai/v1/realtime/sessions?model=gpt-realtime", wantRouted: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &realtimeWebRTCMock{mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}}}
			handler := newRealtimeTestHandler(mock, nil)
			handler.setEnabledPassthroughProviders([]string{"openai"})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)

			_ = handler.ProviderPassthrough(c)

			if !tt.wantRouted {
				if mock.capturedRealtime != nil {
					t.Fatalf("router received %+v, want the request rejected", mock.capturedRealtime)
				}
				if rec.Code != http.StatusNotFound {
					t.Errorf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
				}
				return
			}
			if mock.capturedRealtime == nil {
				t.Fatalf("router received no request (status %d, body %s)", rec.Code, rec.Body.String())
			}
			if mock.capturedRealtime.Intent != tt.wantIntent {
				t.Errorf("intent = %q, want %q", mock.capturedRealtime.Intent, tt.wantIntent)
			}
		})
	}
}

func TestRealtimeMeteredSessionRecordsAudioDuration(t *testing.T) {
	// A translation session reports no usage events at all, so the gateway bills
	// it from the input audio it relays: two seconds of PCM16 at 24 kHz must
	// produce exactly one duration entry when the session ends.
	upstream := newEchoingRealtimeUpstream(t)
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime-translate"}},
		realtimeTarget: &core.RealtimeTarget{
			URL:             "ws" + strings.TrimPrefix(upstream.URL, "http"),
			MeterInputAudio: true,
		},
	}
	usageLogger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newRealtimeTestHandler(mock, usageLogger)

	e := echo.New()
	e.GET("/v1/realtime/translations", handler.RealtimeTranslations)
	gateway := httptest.NewServer(e)
	defer gateway.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/realtime/translations?model=gpt-realtime-translate", nil)
	if err != nil {
		t.Fatalf("client dial failed: %v", err)
	}
	// Audio frames are far larger than the library default read limit; a real
	// realtime client raises it the same way the relay does.
	client.SetReadLimit(realtime.MaxFrameBytes)

	// One second of audio per frame, sent as the translation surface spells it.
	frame, err := json.Marshal(map[string]any{
		"type":  "session.input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(make([]byte, 24000*2)),
	})
	if err != nil {
		t.Fatalf("failed to build frame: %v", err)
	}
	// Each echo confirms the relay consumed the frame the client sent, so the
	// close below cannot outrun the metering of either second of audio.
	for range 2 {
		if err := client.Write(ctx, websocket.MessageText, frame); err != nil {
			t.Fatalf("client write failed: %v", err)
		}
		if _, _, err := client.Read(ctx); err != nil {
			t.Fatalf("client read failed: %v", err)
		}
	}
	_ = client.Close(websocket.StatusNormalClosure, "done")

	entries := waitForUsageEntries(t, usageLogger, 1)
	entry := entries[0]
	if entry.Endpoint != "/v1/realtime/translations" {
		t.Errorf("endpoint = %q, want the translation surface", entry.Endpoint)
	}
	if entry.Model != "gpt-realtime-translate" {
		t.Errorf("model = %q, want the routed model", entry.Model)
	}
	seconds, ok := entry.RawData["audio_seconds"].(float64)
	if !ok {
		t.Fatalf("raw data = %v, want metered audio seconds", entry.RawData)
	}
	if math.Abs(seconds-2) > 1e-9 {
		t.Errorf("audio seconds = %v, want 2", seconds)
	}
}

func TestRealtimeUnmeteredSessionRecordsNoDurationEntry(t *testing.T) {
	// Sessions that report their own usage must not be metered as well: the
	// audio a conversation session streams is already billed by its usage
	// events, and a second entry would double-count it.
	upstream := newEchoingRealtimeUpstream(t)
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider:   &mockProvider{supportedModels: []string{"gpt-realtime"}},
		realtimeTarget: &core.RealtimeTarget{URL: "ws" + strings.TrimPrefix(upstream.URL, "http")},
	}
	usageLogger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newRealtimeTestHandler(mock, usageLogger)

	e := echo.New()
	e.GET("/v1/realtime", handler.Realtime)
	gateway := httptest.NewServer(e)
	defer gateway.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/realtime?model=gpt-realtime", nil)
	if err != nil {
		t.Fatalf("client dial failed: %v", err)
	}
	// Audio frames are far larger than the library default read limit; a real
	// realtime client raises it the same way the relay does.
	client.SetReadLimit(realtime.MaxFrameBytes)
	frame, err := json.Marshal(map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(make([]byte, 24000*2)),
	})
	if err != nil {
		t.Fatalf("failed to build frame: %v", err)
	}
	if err := client.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("client write failed: %v", err)
	}
	if _, _, err := client.Read(ctx); err != nil {
		t.Fatalf("client read failed: %v", err)
	}
	_ = client.Close(websocket.StatusNormalClosure, "done")

	// The session has ended once the upstream sees the connection close.
	select {
	case <-upstream.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session did not end")
	}
	if entries := usageLogger.Entries(); len(entries) != 0 {
		t.Errorf("usage entries = %d, want none for an unmetered session", len(entries))
	}
}

// realtimeUpstream is a websocket server that echoes every frame it receives and
// reports when the session ends. Echoing is what makes metering observable from
// the client side: a reply proves the relay already read — and therefore
// metered — the frame that produced it, so a test never has to race the
// teardown that closing the socket starts.
type realtimeUpstream struct {
	*httptest.Server
	closed chan struct{}
}

func newEchoingRealtimeUpstream(t *testing.T) *realtimeUpstream {
	t.Helper()
	closed := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(realtime.MaxFrameBytes) // audio frames exceed the library default
		defer func() { closed <- struct{}{} }()
		for {
			typ, frame, err := conn.Read(r.Context())
			if err != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			if err := conn.Write(r.Context(), typ, frame); err != nil {
				return
			}
		}
	}))
	return &realtimeUpstream{Server: srv, closed: closed}
}

// waitForUsageEntries blocks until the logger has recorded want entries, which
// happens after the relay tears the session down.
func waitForUsageEntries(t *testing.T, logger *usageCaptureLogger, want int) []*usage.UsageEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if entries := logger.Entries(); len(entries) >= want {
			return entries
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage entries = %d, want %d before timeout", len(logger.Entries()), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
