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

type benchProvider struct{}

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

func (benchProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
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
	auditLogger := benchAuditLogger{cfg: auditlog.Config{Enabled: true, LogBodies: true}}
	usageLogger := benchUsageLogger{cfg: usage.Config{Enabled: true}}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		entry := &auditlog.LogEntry{
			ID:        "audit-bench",
			Timestamp: time.Unix(1700000000, 0),
			RequestID: "req-bench",
			Method:    http.MethodPost,
			Path:      "/v1/chat/completions",
			Data:      &auditlog.LogData{},
		}

		stream := streaming.NewObservedSSEStream(
			io.NopCloser(strings.NewReader(sampleChatStream)),
			auditlog.NewStreamLogObserver(auditLogger, entry, "/v1/chat/completions"),
			usage.NewStreamUsageObserver(
				usageLogger,
				"gpt-4o-mini",
				"mock",
				"req-bench",
				"/v1/chat/completions",
				nil,
			),
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

	// These ceilings are intentionally generous. They are here to catch obvious
	// allocation regressions in the hottest code paths, not to freeze the exact
	// current profile.
	cases := []struct {
		name      string
		bench     func(*testing.B)
		maxAllocs int64
		maxBytes  int64
	}{
		{
			name:      "gateway_chat_completion_hot_path",
			bench:     BenchmarkGatewayHotPathChatCompletion,
			maxAllocs: 130,
			maxBytes:  16 * 1024,
		},
		{
			name:      "openai_responses_stream_converter",
			bench:     BenchmarkOpenAIResponsesStreamConverter,
			maxAllocs: 320,
			maxBytes:  28 * 1024,
		},
		{
			name:      "shared_stream_audit_and_usage_observers",
			bench:     BenchmarkSharedStreamingAuditAndUsageObservers,
			maxAllocs: 175,
			maxBytes:  9 * 1024,
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
