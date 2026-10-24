package llmclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// pacedStreamServer writes each chunk after its delay, flushing every write,
// then holds the connection open for hold (or until the client goes away).
func pacedStreamServer(t *testing.T, hold time.Duration, chunks ...pacedChunk) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		for _, chunk := range chunks {
			select {
			case <-time.After(chunk.delay):
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, chunk.data)
			flusher.Flush()
		}
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type pacedChunk struct {
	delay time.Duration
	data  string
}

func TestClient_DoStream_StallAfterFirstBytesFails(t *testing.T) {
	t.Setenv("HTTP_STREAM_IDLE_TIMEOUT", "100ms")
	server := pacedStreamServer(t, 5*time.Second,
		pacedChunk{data: "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n"},
		pacedChunk{delay: 10 * time.Millisecond, data: "data: {\"choices\":[{\"delta\":{\"content\":\" two\"}}]}\n\n"},
	)
	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)
	defer stream.Close()

	started := time.Now()
	got, err := io.ReadAll(stream)
	assert.Less(t, time.Since(started), 3*time.Second)
	assert.Contains(t, string(got), "one")
	assert.Contains(t, string(got), " two")

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	assert.Contains(t, gatewayErr.Message, "provider stream stalled")
	assert.ErrorIs(t, err, errStreamStalled)
}

func TestClient_DoStream_IdleTimeoutAllowsHealthyStreams(t *testing.T) {
	done := pacedChunk{delay: 60 * time.Millisecond, data: "data: [DONE]\n\n"}
	tests := []struct {
		name    string
		timeout string
		chunks  []pacedChunk
	}{
		{
			name:    "steady chunks within the timeout",
			timeout: "150ms",
			chunks: []pacedChunk{
				{data: "data: {\"n\":1}\n\n"},
				{delay: 60 * time.Millisecond, data: "data: {\"n\":2}\n\n"},
				{delay: 60 * time.Millisecond, data: "data: {\"n\":3}\n\n"},
				done,
			},
		},
		{
			name:    "silence before the first bytes is not counted",
			timeout: "50ms",
			chunks: []pacedChunk{
				{delay: 200 * time.Millisecond, data: "data: {\"n\":1}\n\ndata: [DONE]\n\n"},
			},
		},
		{
			name:    "disabled",
			timeout: "0",
			chunks: []pacedChunk{
				{data: "data: {\"n\":1}\n\n"},
				{delay: 150 * time.Millisecond, data: "data: [DONE]\n\n"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HTTP_STREAM_IDLE_TIMEOUT", tt.timeout)
			server := pacedStreamServer(t, 0, tt.chunks...)
			client := New(DefaultConfig("test", server.URL), nil)

			stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
			require.NoError(t, err)
			defer stream.Close()

			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			assert.Contains(t, string(got), "data: [DONE]")
		})
	}
}

func TestClient_DoStream_IdleTimeoutIgnoresSlowConsumers(t *testing.T) {
	// The provider sends the whole stream at once; the consumer pauses longer
	// than the timeout between reads. Data already received is not silence.
	t.Setenv("HTTP_STREAM_IDLE_TIMEOUT", "50ms")
	server := pacedStreamServer(t, 0, pacedChunk{data: "data: {\"n\":1}\n\ndata: {\"n\":2}\n\ndata: [DONE]\n\n"})
	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)
	defer stream.Close()

	var got []byte
	buf := make([]byte, 8)
	for {
		n, err := stream.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		time.Sleep(80 * time.Millisecond)
	}
	assert.Contains(t, string(got), "data: [DONE]")
}

func TestIdleTimeoutBody_ConcurrentReadAndClose(t *testing.T) {
	// Cancellation closes the stream while a drain goroutine reads it; run
	// under -race, the timer must never be touched unsynchronized.
	for range 200 {
		reader, writer := io.Pipe()
		body := &idleTimeoutBody{ReadCloser: reader, provider: "test", timeout: time.Hour}
		go func() {
			for {
				if _, err := writer.Write([]byte("data")); err != nil {
					return
				}
			}
		}()

		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 4)
			for {
				if _, err := body.Read(buf); err != nil {
					return
				}
			}
		}()

		require.NoError(t, body.Close())
		<-done
		_ = writer.Close()
	}
}
