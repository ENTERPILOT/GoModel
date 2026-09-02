package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestSlowedStreamRecordsUsageConsumedBeforeClientCancellation(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		data     string
	}{
		{
			name:     "chat completions",
			endpoint: "/v1/chat/completions",
			data:     "data: {\"id\":\"chatcmpl-slow\",\"model\":\"gpt-4o\",\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\ndata: [DONE]\n\n",
		},
		{
			name:     "responses",
			endpoint: "/v1/responses",
			data:     "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-slow\",\"model\":\"gpt-4o\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\ndata: [DONE]\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, tt.endpoint, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)
			logger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
			handler := NewHandler(&mockProvider{}, nil, logger, nil)
			source := newDrainSignalingStream(tt.data)

			done := make(chan struct{})
			go func() {
				_ = handler.translatedInference().handleStreamingReadCloser(
					c, nil, gateway.ExecutionMeta{Model: "gpt-4o", ProviderType: "openai", ProviderName: "primary-openai"}, source,
					func(observed io.ReadCloser) io.ReadCloser {
						return streaming.NewSlowdownStream(ctx, observed, 10, time.Now().Add(-time.Second))
					},
				)
				close(done)
			}()

			select {
			case <-source.closed:
			case <-time.After(time.Second):
				t.Fatal("slowdown wrapper did not finish and close the upstream stream")
			}
			entries := logger.Entries()
			if len(entries) != 1 {
				t.Fatalf("usage entries before client delivery = %d, want 1 after provider completion", len(entries))
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("stream handler did not stop after cancellation")
			}

			entries = logger.Entries()
			if len(entries) != 1 {
				t.Fatalf("usage entries = %d, want 1 after provider completed before disconnect", len(entries))
			}
			if entries[0].TotalTokens != 6 {
				t.Fatalf("TotalTokens = %d, want 6", entries[0].TotalTokens)
			}
		})
	}
}

type drainSignalingStream struct {
	reader    *strings.Reader
	closed    chan struct{}
	closeOnce sync.Once
}

func newDrainSignalingStream(data string) *drainSignalingStream {
	return &drainSignalingStream{
		reader: strings.NewReader(data),
		closed: make(chan struct{}),
	}
}

func (s *drainSignalingStream) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *drainSignalingStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}
