package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/tagging"
)

// BenchmarkMiddlewareChainChatCompletion drives a small non-streaming chat
// request through the full server middleware chain (snapshot capture,
// tagging, auth, session detection, workflow resolution) against an
// in-memory provider. It measures per-request overhead that the request
// scope refactor targets, not provider or serialization cost.
func BenchmarkMiddlewareChainChatCompletion(b *testing.B) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		response: &core.ChatResponse{
			ID:      "chatcmpl-bench",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4o-mini",
			Choices: []core.Choice{{
				Index:        0,
				Message:      core.ResponseMessage{Role: "assistant", Content: "Hello!"},
				FinishReason: "stop",
			}},
			Usage: core.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
	srv := New(mock, &Config{
		MasterKey:       "bench-master-key",
		SessionDetector: session.NewDetector(nil, true),
		Tagging:         tagging.NewService([]tagging.Rule{{Header: "X-Team", DoNotPass: true}}, nil),
	})

	// The request logger would otherwise dominate the measurement.
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previous) })

	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer bench-master-key")
		req.Header.Set("X-Team", "platform")
		req.Header.Set(core.UserPathHeader, "/acme/platform")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
}
