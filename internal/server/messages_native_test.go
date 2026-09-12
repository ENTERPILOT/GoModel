package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// A streaming /v1/messages request through the native forwarding path must
// record a usage entry combining message_start input tokens with the final
// message_delta output tokens.
func TestMessages_NativeStreamingLogsUsage(t *testing.T) {
	anthropicSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-fable-5","usage":{"input_tokens":19560,"cache_creation_input_tokens":100,"cache_read_input_tokens":200,"output_tokens":3}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":31}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	provider := &mockProvider{
		supportedModels: []string{"claude-fable-5"},
		providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"text/event-stream; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(anthropicSSE)),
		},
	}
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}

	handler := NewHandler(provider, nil, usageLogger, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.lastPassthroughReq)
	require.Contains(t, rec.Body.String(), "message_stop")
	require.Len(t, usageLogger.entries, 1)

	entry := usageLogger.entries[0]
	assert.Equal(t, 19560, entry.InputTokens)
	assert.Equal(t, 31, entry.OutputTokens)
	assert.Equal(t, 100, entry.RawData["cache_creation_input_tokens"])
	assert.Equal(t, 200, entry.RawData["cache_read_input_tokens"])
}

// A forwarded Accept-Encoding would make the upstream body arrive compressed,
// blinding the SSE usage and audit observers; it must be stripped so the
// transport decompresses transparently.
func TestBuildPassthroughHeadersDropsAcceptEncoding(t *testing.T) {
	src := http.Header{
		"Accept-Encoding": {"gzip, deflate, br, zstd"},
		"Anthropic-Beta":  {"claude-code-20250219"},
	}
	dst := buildPassthroughHeaders(t.Context(), src)
	assert.Empty(t, dst.Get("Accept-Encoding"))
	assert.Equal(t, "claude-code-20250219", dst.Get("Anthropic-Beta"))
}

const anthropicNonStreamingJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"cache_creation_input_tokens":7,"cache_read_input_tokens":9,"output_tokens":25}}`

// A non-streaming /v1/messages request through the native forwarding path
// must record a usage entry from the response object's usage member, so cost
// accounting and budgets see the spend just like on the translated pipeline.
func TestMessages_NativeNonStreamingLogsUsage(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"claude-fable-5"},
		providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(anthropicNonStreamingJSON)),
		},
	}
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}

	handler := NewHandler(provider, nil, usageLogger, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.lastPassthroughReq)
	require.Equal(t, anthropicNonStreamingJSON, rec.Body.String())
	require.Len(t, usageLogger.entries, 1)

	entry := usageLogger.entries[0]
	assert.Equal(t, 100, entry.InputTokens)
	assert.Equal(t, 25, entry.OutputTokens)
	assert.Equal(t, 7, entry.RawData["cache_creation_input_tokens"])
	assert.Equal(t, 9, entry.RawData["cache_read_input_tokens"])
	assert.Equal(t, "msg_1", entry.ProviderID)
}

// A provider body that fails mid-relay must not produce a usage entry: the
// client received an incomplete response and there is no trustworthy usage.
func TestMessages_NativeNonStreamingBodyErrorSkipsUsage(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"claude-fable-5"},
		providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader(anthropicNonStreamingJSON[:40]),
				&failingReader{},
			)),
		},
	}
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}

	handler := NewHandler(provider, nil, usageLogger, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`
	c, _ := echotest.Post(t, "/v1/messages", reqBody)

	require.Error(t, handler.Messages(c))
	require.Empty(t, usageLogger.entries)
}

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

type recordingFeedbackObserver struct {
	input, read, write int
	observed           bool
	calls              int
}

func (r *recordingFeedbackObserver) ObserveResponse(_ context.Context, _ string, _ ext.Endpoint, _ string, _ string, _ string, _ string, inputTokens, cachedInputTokens, cacheWriteInputTokens int, usageObserved bool) {
	r.calls++
	r.input = inputTokens
	r.read = cachedInputTokens
	r.write = cacheWriteInputTokens
	r.observed = usageObserved
}

// Extensions that requested response feedback must receive usage from the
// native SSE stream, with input and cache tokens (message_start) merged with
// the final message_delta.
func TestMessages_NativeStreamingNotifiesFeedbackObservers(t *testing.T) {
	anthropicSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-fable-5","usage":{"input_tokens":19560,"cache_creation_input_tokens":100,"cache_read_input_tokens":200,"output_tokens":3}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":31}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	provider := &mockProvider{
		supportedModels: []string{"claude-fable-5"},
		providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(anthropicSSE)),
		},
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)

	observer := &recordingFeedbackObserver{}
	setResponseFeedbackObservers(c, []ext.ResponseFeedbackObserver{observer})
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, observer.calls)
	assert.True(t, observer.observed)
	assert.Equal(t, 19560, observer.input)
	assert.Equal(t, 200, observer.read)
	assert.Equal(t, 100, observer.write)
}

// Extensions that requested response feedback must also hear about
// non-streaming native responses, with usage read from the response object.
func TestMessages_NativeNonStreamingNotifiesFeedbackObservers(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"claude-fable-5"},
		providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(anthropicNonStreamingJSON)),
		},
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)

	observer := &recordingFeedbackObserver{}
	setResponseFeedbackObservers(c, []ext.ResponseFeedbackObserver{observer})
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, observer.calls)
	assert.True(t, observer.observed)
	assert.Equal(t, 100, observer.input)
	assert.Equal(t, 9, observer.read)
	assert.Equal(t, 7, observer.write)
}

// The capture buffer must abandon oversized bodies without disturbing the
// relay: writes keep succeeding, and Captured reports nothing usable.
func TestCappedCaptureBufferOverflow(t *testing.T) {
	capture := newCappedCaptureBuffer(8)
	for range 3 {
		n, err := capture.Write([]byte("abcde"))
		require.Equal(t, 5, n)
		require.NoError(t, err)
	}
	body, ok := capture.Captured()
	require.False(t, ok, "Captured = (%q, true), want abandoned", body)

	capture = newCappedCaptureBuffer(8)
	_, err := capture.Write([]byte("abcde"))
	require.NoError(t, err)

	body, ok = capture.Captured()
	require.True(t, ok)
	require.Equal(t, "abcde", string(body), "Captured = (%q, %v), want (abcde, true)", body, ok)
}
