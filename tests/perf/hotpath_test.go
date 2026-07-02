package perf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/server"
	"gomodel/internal/streaming"
	"gomodel/internal/usage"
)

const (
	sampleChatRequest = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`
	sampleChatStream  = "" +
		"data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
)

// benchProvider is a mock provider. When models is empty it advertises a single
// default model; otherwise ListModels returns the supplied catalog so the
// registry/router resolution path can be exercised at a realistic catalog size.
type benchProvider struct {
	models []core.Model
}

func (benchProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	model := "gpt-4o-mini"
	if req != nil && req.Model != "" {
		model = req.Model
	}

	return &core.ChatResponse{
		ID:       "chatcmpl-bench",
		Object:   "chat.completion",
		Model:    model,
		Provider: "mock",
		Created:  1700000000,
		Choices: []core.Choice{
			{
				Index:        0,
				FinishReason: "stop",
				Message: core.ResponseMessage{
					Role:    "assistant",
					Content: "Hello!",
				},
			},
		},
		Usage: core.Usage{
			PromptTokens:     5,
			CompletionTokens: 2,
			TotalTokens:      7,
		},
	}, nil
}

func (benchProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(sampleChatStream)), nil
}

func (p benchProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if len(p.models) > 0 {
		return &core.ModelsResponse{Object: "list", Data: p.models}, nil
	}
	return &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{
				ID:      "gpt-4o-mini",
				Object:  "model",
				OwnedBy: "mock",
				Created: 1700000000,
			},
		},
	}, nil
}

func (benchProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	model := "gpt-4o-mini"
	if req != nil && req.Model != "" {
		model = req.Model
	}

	return &core.ResponsesResponse{
		ID:        "resp-bench",
		Object:    "response",
		CreatedAt: 1700000000,
		Model:     model,
		Provider:  "mock",
		Status:    "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:     "msg-bench",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []core.ResponsesContentItem{
					{
						Type: "output_text",
						Text: "Hello!",
					},
				},
			},
		},
		Usage: &core.ResponsesUsage{
			InputTokens:  5,
			OutputTokens: 2,
			TotalTokens:  7,
		},
	}, nil
}

func (benchProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(sampleChatStream)), "gpt-4o-mini", "mock"), nil
}

func (benchProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return &core.EmbeddingResponse{
		Object: "list",
		Model:  "text-embedding-3-small",
		Data: []core.EmbeddingData{
			{
				Object:    "embedding",
				Index:     0,
				Embedding: []byte(`[0.1,0.2,0.3]`),
			},
		},
		Usage: core.EmbeddingUsage{
			PromptTokens: 5,
			TotalTokens:  5,
		},
	}, nil
}

func (benchProvider) Supports(model string) bool {
	return strings.TrimSpace(model) != ""
}

func (benchProvider) GetProviderType(_ string) string {
	return "mock"
}

type benchAuditLogger struct {
	cfg auditlog.Config
}

func (l benchAuditLogger) Write(_ *auditlog.LogEntry) {}
func (l benchAuditLogger) Config() auditlog.Config    { return l.cfg }
func (l benchAuditLogger) Close() error               { return nil }

type benchUsageLogger struct {
	cfg usage.Config
}

func (l benchUsageLogger) Write(_ *usage.UsageEntry) {}
func (l benchUsageLogger) Config() usage.Config      { return l.cfg }
func (l benchUsageLogger) Close() error              { return nil }

func TestMain(m *testing.M) {
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	code := m.Run()
	slog.SetDefault(original)
	os.Exit(code)
}

// BenchmarkGatewayHotPathChatCompletion measures the pipeline overhead with a
// bare provider (no Router/registry). It isolates serialization + middleware
// cost; it does NOT cover model resolution. See the Routed variant for the
// production-shaped path.
func BenchmarkGatewayHotPathChatCompletion(b *testing.B) {
	srv := server.New(benchProvider{}, &server.Config{LogOnlyModelInteractions: true})
	body := []byte(sampleChatRequest)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	}
}

// routedCatalogSize is a representative multi-provider catalog size. Real
// deployments aggregating several upstreams routinely exceed this.
const routedCatalogSize = 256

// newRoutedBenchServer wires the server the way production does: through a real
// ModelRegistry + Router populated with `modelCount` models. Unlike passing a
// bare provider to server.New, this exercises the per-request model-resolution
// path (ResolveModel/Supports/GetProviderType/GetProviderName), which is where
// catalog-sized allocation and CPU cost actually live.
func newRoutedBenchServer(tb testing.TB, modelCount int) *server.Server {
	tb.Helper()

	models := make([]core.Model, 0, modelCount)
	models = append(models, core.Model{ID: "gpt-4o-mini", Object: "model", OwnedBy: "mock", Created: 1700000000})
	for i := 1; i < modelCount; i++ {
		models = append(models, core.Model{
			ID:      fmt.Sprintf("filler-model-%04d", i),
			Object:  "model",
			OwnedBy: "mock",
			Created: 1700000000,
		})
	}

	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&benchProvider{models: models}, "mock", "mock")
	if err := registry.Initialize(context.Background()); err != nil {
		tb.Fatalf("registry initialize: %v", err)
	}

	router, err := providers.NewRouter(registry)
	if err != nil {
		tb.Fatalf("new router: %v", err)
	}

	return server.New(router, &server.Config{LogOnlyModelInteractions: true})
}

// BenchmarkGatewayHotPathChatCompletionRouted measures the hot path through a
// real Router with a realistic catalog. Compare against
// BenchmarkGatewayHotPathChatCompletion (bare provider, no routing) to see the
// cost the routing/resolution layer adds per request.
func BenchmarkGatewayHotPathChatCompletionRouted(b *testing.B) {
	srv := newRoutedBenchServer(b, routedCatalogSize)
	body := []byte(sampleChatRequest)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	}
}

func BenchmarkOpenAIResponsesStreamConverter(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		converter := providers.NewOpenAIResponsesStreamConverter(
			io.NopCloser(strings.NewReader(sampleChatStream)),
			"gpt-4o-mini",
			"mock",
		)

		if _, err := io.Copy(io.Discard, converter); err != nil {
			b.Fatalf("drain converter: %v", err)
		}
		if err := converter.Close(); err != nil {
			b.Fatalf("close converter: %v", err)
		}
	}
}

func BenchmarkSharedStreamingAuditAndUsageObservers(b *testing.B) {
	benchmarkSharedStreamingObservers(b, auditlog.Config{Enabled: true, LogBodies: true})
}

// BenchmarkSharedStreamingObserversDefaultConfig runs the same pipeline with
// audit body capture disabled — the default configuration, where the stream
// can skip decoding content-delta chunks.
func BenchmarkSharedStreamingObserversDefaultConfig(b *testing.B) {
	benchmarkSharedStreamingObservers(b, auditlog.Config{Enabled: true})
}

func benchmarkSharedStreamingObservers(b *testing.B, auditCfg auditlog.Config) {
	auditLogger := benchAuditLogger{cfg: auditCfg}
	usageLogger := benchUsageLogger{cfg: usage.Config{Enabled: true}}
	// Labels mirror a tagged request so the guard exercises the labelled path.
	labels := []string{"team-alpha", "prod"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		entry := &auditlog.LogEntry{
			ID:        "audit-bench",
			Timestamp: time.Unix(1700000000, 0),
			RequestID: "req-bench",
			Method:    http.MethodPost,
			Path:      "/v1/chat/completions",
			Data:      &auditlog.LogData{Labels: labels},
		}

		usageObserver := usage.NewStreamUsageObserver(
			usageLogger,
			"gpt-4o-mini",
			"mock",
			"req-bench",
			"/v1/chat/completions",
			nil,
		)
		usageObserver.SetLabels(labels)

		stream := streaming.NewObservedSSEStream(
			io.NopCloser(strings.NewReader(sampleChatStream)),
			auditlog.NewStreamLogObserver(auditLogger, entry, "/v1/chat/completions"),
			usageObserver,
		)

		if _, err := io.Copy(io.Discard, stream); err != nil {
			b.Fatalf("drain wrapped stream: %v", err)
		}
		if err := stream.Close(); err != nil {
			b.Fatalf("close wrapped stream: %v", err)
		}
	}
}

func TestFormatPerfGuardResult(t *testing.T) {
	result := testing.BenchmarkResult{
		N:         1,
		T:         2 * time.Microsecond,
		MemAllocs: 114,
		MemBytes:  13654,
	}

	got := formatPerfGuardResult("gateway_chat_completion_hot_path", result, 150, 18*1024)

	for _, want := range []string{
		"gateway_chat_completion_hot_path",
		"ns/op=",
		"allocs/op=114/150",
		"bytes/op=13654/18432",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatPerfGuardResult() = %q, want substring %q", got, want)
		}
	}
}

func formatPerfGuardResult(name string, result testing.BenchmarkResult, maxAllocs, maxBytes int64) string {
	return fmt.Sprintf(
		"%s: ns/op=%d allocs/op=%d/%d bytes/op=%d/%d",
		name,
		result.NsPerOp(),
		result.AllocsPerOp(),
		maxAllocs,
		result.AllocedBytesPerOp(),
		maxBytes,
	)
}

func TestHotPathPerfGuard(t *testing.T) {
	t.Helper()

	// Ceilings sit ~5% above the measured baseline: tight enough to catch real
	// allocation regressions, loose enough to absorb minor Go/dependency drift.
	// Allocation counts here are deterministic and match across architectures
	// (linux/amd64 CI == darwin/arm64 local), so these are stable. When a change
	// legitimately adds allocations, re-measure with `make perf-bench` and bump
	// the affected ceiling in the same commit.
	cases := []struct {
		name      string
		bench     func(*testing.B)
		maxAllocs int64
		maxBytes  int64
	}{
		{
			name:      "gateway_chat_completion_hot_path",
			bench:     BenchmarkGatewayHotPathChatCompletion,
			maxAllocs: 110,   // baseline 108
			maxBytes:  14080, // baseline ~13.5 KB (incl. per-attempt response body/header capture fields)
		},
		{
			// Production-shaped path: request resolves through a real Router +
			// catalog. Resolution uses an O(1) selector index, so the ceilings
			// sit close to the bare-provider case and are independent of catalog
			// size. A regression to catalog-scanning resolution (which copied the
			// full catalog several times per request) would blow these limits.
			name:      "gateway_chat_completion_hot_path_routed",
			bench:     BenchmarkGatewayHotPathChatCompletionRouted,
			maxAllocs: 128,   // baseline 126
			maxBytes:  14656, // baseline ~14.0 KB
		},
		{
			// Typed chunk decoding + reused read buffer keep this converter at a
			// fraction of its former map[string]any-per-chunk cost (was 202/19.6KB).
			name:      "openai_responses_stream_converter",
			bench:     BenchmarkOpenAIResponsesStreamConverter,
			maxAllocs: 87,    // baseline 85
			maxBytes:  10752, // baseline ~9.7 KB (leaves headroom for pool cold-starts)
		},
		{
			name:      "shared_stream_audit_and_usage_observers",
			bench:     BenchmarkSharedStreamingAuditAndUsageObservers,
			maxAllocs: 157,  // baseline 155 (incl. request labels on both observers)
			maxBytes:  8960, // baseline ~8.5 KB
		},
		{
			// Default configuration: audit body capture off, so the observed
			// stream must skip JSON decoding for content-delta chunks entirely.
			// A regression that decodes every chunk again would blow this limit.
			name:      "shared_stream_observers_default_config",
			bench:     BenchmarkSharedStreamingObserversDefaultConfig,
			maxAllocs: 62,   // baseline 60 (incl. request labels on both observers)
			maxBytes:  3584, // baseline ~3.3 KB
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := testing.Benchmark(tc.bench)
			t.Log(formatPerfGuardResult(tc.name, result, tc.maxAllocs, tc.maxBytes))

			if got := result.AllocsPerOp(); got > tc.maxAllocs {
				t.Fatalf("allocs/op = %d, want <= %d", got, tc.maxAllocs)
			}
			if got := result.AllocedBytesPerOp(); got > tc.maxBytes {
				t.Fatalf("bytes/op = %d, want <= %d", got, tc.maxBytes)
			}
		})
	}
}
