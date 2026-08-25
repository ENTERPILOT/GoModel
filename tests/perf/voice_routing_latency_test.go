package perf

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/enterpilot/gomodel/internal/providers"
	openai_provider "github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/server"
)

const (
	voiceLatencySamples     = 31
	maxVoiceRoutingOverhead = 5 * time.Millisecond
)

// TestVoiceRoutingLatency measures only GoModel's added latency by comparing the
// same request sent directly to a zero-delay mock provider and routed through the
// gateway. The median keeps this guard stable on shared CI runners while still
// catching millisecond-scale regressions in the three voice paths.
func TestVoiceRoutingLatency(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(mockVoiceProvider))
	t.Cleanup(providerServer.Close)

	provider := openai_provider.New(providers.ProviderConfig{
		APIKey:  "mock-key",
		BaseURL: providerServer.URL + "/v1",
	}, providers.ProviderOptions{})
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "mock-openai", "openai")
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mock provider: %v", err)
	}
	router, err := providers.NewRouter(registry)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	gateway := httptest.NewServer(server.New(router, &server.Config{
		LogOnlyModelInteractions: true,
		RealtimeEnabled:          true,
	}))
	t.Cleanup(gateway.Close)

	t.Run("tts", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy","response_format":"wav"}`)
		measureHTTPRoutingOverhead(t, providerServer.Client(), providerServer.URL+"/v1/audio/speech", gateway.URL+"/v1/audio/speech", "application/json", body)
	})

	t.Run("stt", func(t *testing.T) {
		body, contentType := transcriptionBody(t)
		measureHTTPRoutingOverhead(t, providerServer.Client(), providerServer.URL+"/v1/audio/transcriptions", gateway.URL+"/v1/audio/transcriptions", contentType, body)
	})

	t.Run("realtime", func(t *testing.T) {
		directURL := websocketURL(providerServer.URL + "/v1/realtime?model=gpt-realtime-mini")
		routedURL := websocketURL(gateway.URL + "/v1/realtime?model=gpt-realtime-mini")
		measureWebsocketRoutingOverhead(t, directURL, routedURL)
	})
}

func mockVoiceProvider(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/models":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini-tts","object":"model","owned_by":"openai"},{"id":"gpt-4o-transcribe","object":"model","owned_by":"openai"},{"id":"gpt-realtime-mini","object":"model","owned_by":"openai"}]}`)
	case "/v1/audio/speech":
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("mock-audio"))
	case "/v1/audio/transcriptions":
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello"}`)
	case "/v1/realtime":
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func transcriptionBody(tb testing.TB) ([]byte, string) {
	tb.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-4o-transcribe"); err != nil {
		tb.Fatalf("write model field: %v", err)
	}
	file, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		tb.Fatalf("create audio field: %v", err)
	}
	if _, err := file.Write([]byte("mock-wave-data")); err != nil {
		tb.Fatalf("write audio field: %v", err)
	}
	if err := writer.Close(); err != nil {
		tb.Fatalf("close multipart body: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func measureHTTPRoutingOverhead(t *testing.T, client *http.Client, directURL, routedURL, contentType string, body []byte) {
	t.Helper()

	direct := make([]time.Duration, 0, voiceLatencySamples)
	routed := make([]time.Duration, 0, voiceLatencySamples)
	for i := range voiceLatencySamples + 2 {
		directDuration := timedHTTPRequest(t, client, directURL, contentType, body)
		routedDuration := timedHTTPRequest(t, client, routedURL, contentType, body)
		if i >= 2 { // warm both persistent connections before collecting samples
			direct = append(direct, directDuration)
			routed = append(routed, routedDuration)
		}
	}
	assertVoiceRoutingOverhead(t, median(direct), median(routed))
}

func timedHTTPRequest(t *testing.T, client *http.Client, url, contentType string, body []byte) time.Duration {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("read response from %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request %s returned %s", url, resp.Status)
	}
	return time.Since(started)
}

func measureWebsocketRoutingOverhead(t *testing.T, directURL, routedURL string) {
	t.Helper()

	direct := make([]time.Duration, 0, voiceLatencySamples)
	routed := make([]time.Duration, 0, voiceLatencySamples)
	for i := range voiceLatencySamples + 2 {
		directDuration := timedWebsocketDial(t, directURL)
		routedDuration := timedWebsocketDial(t, routedURL)
		if i >= 2 {
			direct = append(direct, directDuration)
			routed = append(routed, routedDuration)
		}
	}
	assertVoiceRoutingOverhead(t, median(direct), median(routed))
}

func timedWebsocketDial(t *testing.T, url string) time.Duration {
	t.Helper()

	started := time.Now()
	conn, _, err := websocket.Dial(context.Background(), url, nil)
	duration := time.Since(started)
	if err != nil {
		t.Fatalf("websocket dial %s: %v", url, err)
	}
	_ = conn.CloseNow()
	return duration
}

func assertVoiceRoutingOverhead(t *testing.T, direct, routed time.Duration) {
	t.Helper()

	overhead := max(routed-direct, 0)
	t.Logf("direct_p50=%s routed_p50=%s gomodel_overhead_p50=%s threshold=%s", direct, routed, overhead, maxVoiceRoutingOverhead)
	if overhead > maxVoiceRoutingOverhead {
		t.Fatalf("GoModel median routing overhead = %s, want <= %s", overhead, maxVoiceRoutingOverhead)
	}
}

func median(samples []time.Duration) time.Duration {
	slices.Sort(samples)
	return samples[len(samples)/2]
}

func websocketURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}
