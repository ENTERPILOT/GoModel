package responsecache

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// missStore is a cache.Store that never holds an entry, so every request
// takes the full miss path: lookup, provider call, capture and write.
type missStore struct{}

func (missStore) Get(context.Context, string) ([]byte, error)              { return nil, nil }
func (missStore) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (missStore) Close() error                                             { return nil }

func BenchmarkHandleExactMiss(b *testing.B) {
	content := strings.Repeat("Summarize the quarterly report section by section. ", 700)
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"You are concise."},{"role":"user","content":"` + content + `"}],"temperature":0.2}`)
	mw := &ResponseCacheMiddleware{simple: newSimpleCacheMiddleware(missStore{}, time.Hour, nil)}
	defer mw.Close()
	ex := &blockingMissExchange{ctx: context.Background()}
	next := func() error { return nil }
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := mw.handle(ex, body, next); err != nil {
			b.Fatal(err)
		}
	}
}

// sseStreamOfSize builds a cacheable chat stream of at least size bytes as
// separate events, so a capture sees it arrive write by write.
func sseStreamOfSize(size int) [][]byte {
	event := []byte(`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"` + strings.Repeat("x", 1000) + `"}}]}` + "\n\n")
	var events [][]byte
	for total := 0; total < size; total += len(event) {
		events = append(events, event)
	}
	return append(events, []byte("data: [DONE]\n\n"))
}

// sseStreamOfExactSize builds a cacheable chat stream of exactly size bytes,
// padding it with an SSE comment before the final [DONE] event.
func sseStreamOfExactSize(t *testing.T, size int) [][]byte {
	t.Helper()
	events := sseStreamOfSize(size - 4096)
	done := events[len(events)-1]
	events = events[:len(events)-1]
	padLen := size - len(bytes.Join(events, nil)) - len(done)
	require.GreaterOrEqual(t, padLen, 4)
	padding := []byte(": " + strings.Repeat(" ", padLen-4) + "\n\n")
	events = append(events, padding, done)
	require.Len(t, bytes.Join(events, nil), size)
	return events
}

func TestHandleRequest_CapturedStreamSizeLimit(t *testing.T) {
	largeJSON := []byte(`{"data":"` + strings.Repeat("x", maxCapturedStreamBytes+1024) + `"}`)
	tests := []struct {
		name       string
		stream     bool
		writes     [][]byte
		wantCached bool
	}{
		{name: "stream under the limit is cached", stream: true, writes: sseStreamOfSize(maxCapturedStreamBytes - 4096), wantCached: true},
		{name: "stream at exactly the limit is cached", stream: true, writes: sseStreamOfExactSize(t, maxCapturedStreamBytes), wantCached: true},
		{name: "stream over the limit is not cached", stream: true, writes: sseStreamOfSize(maxCapturedStreamBytes + 4096)},
		{name: "non-streaming body over the limit is still cached", writes: [][]byte{largeJSON}, wantCached: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cache.NewMapStore()
			mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
			t.Cleanup(func() { _ = mw.Close() })
			workflow := resolvedWorkflow("openai", "gpt-4")
			body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"size limit"}]}`)
			contentType := "application/json"
			if tt.stream {
				body = []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"size limit"}]}`)
				contentType = "text/event-stream"
			}
			want := bytes.Join(tt.writes, nil)
			calls := 0
			next := func(c *echo.Context) error {
				calls++
				c.Response().Header().Set("Content-Type", contentType)
				c.Response().WriteHeader(http.StatusOK)
				for _, w := range tt.writes {
					_, err := c.Response().Write(w)
					require.NoError(t, err)
				}
				return nil
			}

			first := driveHandleRequest(t, mw, workflow, body, nil, next)
			assert.Empty(t, first.Header().Get("X-Cache"))
			assert.Equal(t, want, first.Body.Bytes(), "the client receives the whole response")
			mw.simple.wg.Wait()

			second := driveHandleRequest(t, mw, workflow, body, nil, next)
			assert.Equal(t, want, second.Body.Bytes())
			if tt.wantCached {
				assert.Equal(t, CacheHeaderExact, second.Header().Get("X-Cache"))
				assert.Equal(t, 1, calls)
				return
			}
			assert.Empty(t, second.Header().Get("X-Cache"))
			assert.Equal(t, 2, calls)
		})
	}
}

func TestCaptureResponseForCache_NestedCapture(t *testing.T) {
	tests := []struct {
		name string
		// outerFirst writes the status through the outer capture before the
		// inner capture starts, so the inner one cannot reuse it.
		outerFirst bool
		wantShared bool
	}{
		{name: "untouched outer capture is reused", wantShared: true},
		{name: "outer capture already written to gets its own inner capture", outerFirst: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := echotest.Post(t, "/v1/chat/completions", nil)
			var inner []byte
			outer, outerOK, err := captureResponseForCache(c, "/v1/chat/completions", "outer", func() error {
				if tt.outerFirst {
					c.Response().WriteHeader(http.StatusOK)
				}
				var innerOK bool
				var innerErr error
				inner, innerOK, innerErr = captureResponseForCache(c, "/v1/chat/completions", "inner", func() error {
					return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
				})
				require.NoError(t, innerErr)
				require.True(t, innerOK)
				return nil
			})
			require.NoError(t, err)
			require.True(t, outerOK)
			require.NotEmpty(t, inner)
			require.NotEmpty(t, outer)

			assert.Equal(t, rec.Body.Bytes(), inner)
			assert.Equal(t, rec.Body.Bytes(), outer)
			if tt.wantShared {
				assert.Same(t, &outer[0], &inner[0], "both layers read one buffer")
				return
			}
			assert.NotSame(t, &outer[0], &inner[0])
		})
	}
}
