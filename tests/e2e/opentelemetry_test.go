//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/enterpilot/gomodel/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	otelTraceID  = "463ac35c9f6413ad48485a3953bb6124"
	otelParentID = "a2fb4a1d1a96d312"
)

// TestOpenTelemetryExport boots a complete gateway through run.Run with
// OTEL_ENABLED against an in-memory OTLP collector, and checks what the
// gateway exports — and, just as importantly, what it never exports.
func TestOpenTelemetryExport(t *testing.T) {
	collector := newOTLPTestCollector()
	t.Cleanup(collector.Close)
	upstream := httptest.NewServer(http.HandlerFunc(otelStubUpstream))
	t.Cleanup(upstream.Close)

	gateway := startOTelGateway(t, collector.URL(), upstream.URL)

	t.Run("buffered call exports a client span and duration", func(t *testing.T) {
		resp := sendJSONRequest(t, gateway+"/v1/chat/completions", map[string]any{
			"model":    "otel-buffered",
			"messages": []map[string]any{{"role": "user", "content": "hello"}},
		})
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "buffered call returned %d: %s", resp.StatusCode, body)

		expected := map[string]any{
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "otel-buffered",
			"gomodel.provider.name": "vllm-eu",
		}
		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.findSpan(func(span *tracepb.Span) bool {
				return span.Name == "chat otel-buffered" && attributesContain(span.Attributes, expected)
			}) != nil
		}))

		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.hasHistogramPoint("gen_ai.client.operation.duration", expected)
		}))

	})

	t.Run("provider failure exports bounded error telemetry", func(t *testing.T) {
		resp := sendJSONRequest(t, gateway+"/v1/chat/completions", map[string]any{
			"model":    "otel-failure",
			"messages": []map[string]any{{"role": "user", "content": "failure-prompt-secret"}},
		})
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

		expected := map[string]any{
			"error.type":            "429",
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "otel-failure",
			"gomodel.provider.name": "vllm-eu",
		}
		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.findSpan(func(span *tracepb.Span) bool {
				return span.Name == "chat otel-failure" &&
					attributesContain(span.Attributes, expected) &&
					attributesContain(span.Attributes, map[string]any{"http.response.status_code": int64(http.StatusTooManyRequests)})
			}) != nil
		}))

		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.hasHistogramPoint("gen_ai.client.operation.duration", expected)
		}))

		for _, secret := range []string{"failure-prompt-secret", "upstream-secret-never-export"} {
			require.False(t, collector.containsString(secret), "failure detail %q appeared in telemetry", secret)
		}
	})

	t.Run("translated stream records time to first chunk", func(t *testing.T) {
		resp := sendJSONRequest(t, gateway+"/v1/chat/completions", map[string]any{
			"model":    "otel-stream",
			"stream":   true,
			"messages": []map[string]any{{"role": "user", "content": "hello"}},
		})
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), string([]byte("[DONE]")))

		expected := map[string]any{
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "otel-stream",
			"gen_ai.request.stream": true,
			"gomodel.provider.name": "vllm-eu",
		}
		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.hasHistogramPoint("gen_ai.client.operation.time_to_first_chunk", expected)
		}))

	})

	t.Run("passthrough stream keeps GenAI metadata and joins the caller's trace", func(t *testing.T) {
		secretPrompt := "prompt-secret-never-export"
		body := fmt.Sprintf(`{"model":"otel-passthrough","padding":%q,"stream":true}`, secretPrompt+strings.Repeat("x", 70*1024))
		req, err := http.NewRequest(http.MethodPost, gateway+"/p/vllm/chat/completions", bytes.NewBufferString(body))
		require.NoError(t, err)

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-B3-TraceId", otelTraceID)
		req.Header.Set("X-B3-SpanId", otelParentID)
		req.Header.Set("X-B3-Sampled", "1")
		req.Header.Set("User-Agent", "identity-secret-agent")
		req.Header.Set("X-Forwarded-For", "198.51.100.23")
		req.Host = "attacker-cardinality.example"

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		responseBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(responseBody), string([]byte("[DONE]")))

		metricAttributes := map[string]any{
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "otel-passthrough",
			"gen_ai.request.stream": true,
			"gomodel.provider.name": "vllm-eu",
		}
		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.hasHistogramPoint("gen_ai.client.operation.time_to_first_chunk", metricAttributes)
		}))

		var serverSpan *tracepb.Span
		require.True(t, collector.waitFor(5*time.Second, func() bool {
			serverSpan = collector.findSpan(func(span *tracepb.Span) bool {
				return hex.EncodeToString(span.TraceId) == otelTraceID && span.Name == "POST /p/:provider/*"
			})
			return serverSpan != nil
		}))
		got := hex.EncodeToString(serverSpan.ParentSpanId)
		require.Equal(t, otelParentID, got)

		for _, key := range []string{"client.address", "network.peer.address", "network.peer.port", "server.address", "server.port", "user_agent.original"} {
			for _, attribute := range serverSpan.Attributes {
				require.NotEqual(t, key, attribute.Key)
			}
		}
		for _, secret := range []string{secretPrompt, "identity-secret-agent", "attacker-cardinality.example", "198.51.100.23"} {
			require.False(t, collector.containsString(secret), "identity or prompt value %q appeared in telemetry", secret)
		}
	})

	t.Run("stream that ends before its first chunk is exported as a failure", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, gateway+"/p/vllm/chat/completions", strings.NewReader(`{"model":"otel-empty-stream","stream":true}`))
		require.NoError(t, err)

		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "empty stream returned %d: %s", resp.StatusCode, body)

		expected := map[string]any{
			"error.type":            "empty_stream",
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "otel-empty-stream",
			"gomodel.provider.name": "vllm-eu",
		}
		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.hasHistogramPoint("gen_ai.client.operation.duration", expected)
		}))

		require.True(t, collector.waitFor(5*time.Second, func() bool {
			return collector.findSpan(func(span *tracepb.Span) bool {
				return span.Name == "chat otel-empty-stream" && attributesContain(span.Attributes, expected)
			}) != nil
		}))

	})

	t.Run("operational endpoints are excluded", func(t *testing.T) {
		for _, path := range []string{"/health", "/health/ready", "/monitoring/metrics", "/debug/pprof/"} {
			resp, err := http.Get(gateway + path)
			require.NoError(t, err, "GET %s: %v", path, err)

			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s returned %d", path, resp.StatusCode)
		}
		time.Sleep(200 * time.Millisecond)
		for _, route := range []string{"GET /health", "GET /health/ready", "GET /monitoring/metrics", "GET /debug/pprof/"} {
			span := collector.findSpan(func(span *tracepb.Span) bool { return span.Name == route })
			require.Nil(t, span)
		}
	})
}

// startOTelGateway runs a gateway generation on a free loopback port with
// OpenTelemetry export pointed at collectorURL and a single vLLM-compatible
// provider pointed at upstreamURL. It returns the gateway base URL.
func startOTelGateway(t *testing.T, collectorURL, upstreamURL string) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	gateway := "http://127.0.0.1:" + port

	tmp := t.TempDir()
	for key, value := range map[string]string{
		"PORT":                            port,
		"PID_FILE":                        tmp + "/gomodel.pid",
		"SQLITE_PATH":                     tmp + "/gomodel.db",
		"LOG_LEVEL":                       "warn",
		"GOMODEL_VERSION_CHECK_ENABLED":   "false",
		"VLLM_EU_BASE_URL":                upstreamURL,
		"VLLM_EU_API_KEY":                 "sk-e2e",
		"VLLM_EU_MODELS":                  "otel-buffered,otel-stream,otel-failure,otel-passthrough,otel-empty-stream",
		"CONFIGURED_PROVIDER_MODELS_MODE": "allowlist",
		"RETRY_MAX_RETRIES":               "0",
		"METRICS_ENABLED":                 "true",
		"METRICS_ENDPOINT":                "monitoring/metrics",
		"PPROF_ENABLED":                   "true",
		"OTEL_ENABLED":                    "true",
		"OTEL_SERVICE_NAME":               "gomodel-e2e",
		"OTEL_EXPORTER_OTLP_ENDPOINT":     collectorURL,
		"OTEL_EXPORTER_OTLP_PROTOCOL":     "http/protobuf",
		"OTEL_TRACES_SAMPLER":             "always_on",
		"OTEL_PROPAGATORS":                "b3multi,baggage",
		"OTEL_BSP_SCHEDULE_DELAY":         "20",
		"OTEL_METRIC_EXPORT_INTERVAL":     "20",
	} {
		t.Setenv(key, value)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run.Run(ctx, run.Options{ProductName: "gomodel-e2e", Args: []string{}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)

		case <-time.After(10 * time.Second):
			t.Error("gateway shutdown timed out")
		}
	})
	err = waitForServer(gateway + "/health")
	require.NoError(t, err)

	return gateway
}

// otelStubUpstream is an OpenAI-compatible upstream: a 429 with a secret
// error message for "otel-failure", an SSE stream for stream requests, and a
// buffered completion otherwise.
func otelStubUpstream(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Model == "otel-failure" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"upstream-secret-never-export","type":"rate_limit_error"}}`)
			return
		}
		if request.Model == "otel-empty-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			flusher.Flush()
			time.Sleep(25 * time.Millisecond)
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-e2e-stream\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stub answer\"},\"finish_reason\":null}]}\n\n", request.Model)
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-e2e","object":"chat.completion","created":1700000000,"model":%q,
			"choices":[{"index":0,"message":{"role":"assistant","content":"stub answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, request.Model)
	case strings.HasSuffix(r.URL.Path, "/models"):
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"otel-buffered","object":"model"}]}`)
	default:
		http.NotFound(w, r)
	}
}
