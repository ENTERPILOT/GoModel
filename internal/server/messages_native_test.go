package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

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
