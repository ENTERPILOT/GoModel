package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
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

	e := echo.New()
	handler := NewHandler(provider, nil, usageLogger, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("native path was not taken")
	}
	if !strings.Contains(rec.Body.String(), "message_stop") {
		t.Fatalf("stream not relayed: %s", rec.Body.String())
	}

	if len(usageLogger.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLogger.entries))
	}
	entry := usageLogger.entries[0]
	if entry.InputTokens != 19560 {
		t.Errorf("InputTokens = %d, want 19560", entry.InputTokens)
	}
	if entry.OutputTokens != 31 {
		t.Errorf("OutputTokens = %d, want 31", entry.OutputTokens)
	}
	if entry.RawData["cache_creation_input_tokens"] != 100 {
		t.Errorf("cache_creation_input_tokens = %v, want 100", entry.RawData["cache_creation_input_tokens"])
	}
	if entry.RawData["cache_read_input_tokens"] != 200 {
		t.Errorf("cache_read_input_tokens = %v, want 200", entry.RawData["cache_read_input_tokens"])
	}
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
	if got := dst.Get("Accept-Encoding"); got != "" {
		t.Errorf("Accept-Encoding forwarded as %q, want stripped", got)
	}
	if got := dst.Get("Anthropic-Beta"); got != "claude-code-20250219" {
		t.Errorf("Anthropic-Beta = %q, want preserved", got)
	}
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

	e := echo.New()
	handler := NewHandler(provider, nil, usageLogger, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("native path was not taken")
	}
	if rec.Body.String() != anthropicNonStreamingJSON {
		t.Fatalf("body not relayed verbatim: %s", rec.Body.String())
	}

	if len(usageLogger.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLogger.entries))
	}
	entry := usageLogger.entries[0]
	if entry.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", entry.InputTokens)
	}
	if entry.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25", entry.OutputTokens)
	}
	if entry.RawData["cache_creation_input_tokens"] != 7 {
		t.Errorf("cache_creation_input_tokens = %v, want 7", entry.RawData["cache_creation_input_tokens"])
	}
	if entry.RawData["cache_read_input_tokens"] != 9 {
		t.Errorf("cache_read_input_tokens = %v, want 9", entry.RawData["cache_read_input_tokens"])
	}
	if entry.ProviderID != "msg_1" {
		t.Errorf("ProviderID = %q, want msg_1", entry.ProviderID)
	}
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

	e := echo.New()
	handler := NewHandler(provider, nil, usageLogger, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err == nil {
		t.Fatal("Messages: expected relay error, got nil")
	}
	if len(usageLogger.entries) != 0 {
		t.Fatalf("usage entries = %d, want 0", len(usageLogger.entries))
	}
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

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	observer := &recordingFeedbackObserver{}
	setResponseFeedbackObservers(c, []ext.ResponseFeedbackObserver{observer})

	if err := handler.Messages(c); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if observer.calls != 1 {
		t.Fatalf("ObserveResponse calls = %d, want 1", observer.calls)
	}
	if !observer.observed {
		t.Error("usageObserved = false, want true")
	}
	if observer.input != 19560 {
		t.Errorf("inputTokens = %d, want 19560", observer.input)
	}
	if observer.read != 200 {
		t.Errorf("cachedInputTokens = %d, want 200", observer.read)
	}
	if observer.write != 100 {
		t.Errorf("cacheWriteInputTokens = %d, want 100", observer.write)
	}
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

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	observer := &recordingFeedbackObserver{}
	setResponseFeedbackObservers(c, []ext.ResponseFeedbackObserver{observer})

	if err := handler.Messages(c); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if observer.calls != 1 {
		t.Fatalf("ObserveResponse calls = %d, want 1", observer.calls)
	}
	if !observer.observed {
		t.Error("usageObserved = false, want true")
	}
	if observer.input != 100 {
		t.Errorf("inputTokens = %d, want 100", observer.input)
	}
	if observer.read != 9 {
		t.Errorf("cachedInputTokens = %d, want 9", observer.read)
	}
	if observer.write != 7 {
		t.Errorf("cacheWriteInputTokens = %d, want 7", observer.write)
	}
}

// The capture buffer must abandon oversized bodies without disturbing the
// relay: writes keep succeeding, and Captured reports nothing usable.
func TestCappedCaptureBufferOverflow(t *testing.T) {
	capture := newCappedCaptureBuffer(8)
	for range 3 {
		n, err := capture.Write([]byte("abcde"))
		if n != 5 || err != nil {
			t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
		}
	}
	if body, ok := capture.Captured(); ok {
		t.Fatalf("Captured = (%q, true), want abandoned", body)
	}

	capture = newCappedCaptureBuffer(8)
	if _, err := capture.Write([]byte("abcde")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, ok := capture.Captured()
	if !ok || string(body) != "abcde" {
		t.Fatalf("Captured = (%q, %v), want (abcde, true)", body, ok)
	}
}
