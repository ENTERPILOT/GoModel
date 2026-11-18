package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestBuildPassthroughHeadersSkipsConfiguredUserPathHeader(t *testing.T) {
	ctx := core.WithUserPathHeaderName(context.Background(), "X-Tenant-Path")
	headers := http.Header{}
	headers.Set("X-Tenant-Path", "/team/alpha")
	headers.Set(core.UserPathHeader, "/team/default")
	headers.Set("OpenAI-Beta", "responses=v1")

	got := buildPassthroughHeaders(ctx, headers)
	value := got.Get("X-Tenant-Path")
	require.Empty(t, value)
	value = got.Get(core.UserPathHeader)
	require.Empty(t, value, "%s should not be forwarded, got %q", core.UserPathHeader, value)
	value = got.Get("OpenAI-Beta")
	require.Equal(t, "responses=v1", value)
}

// TestDefaultEnabledPassthroughProvidersIncludesMatrixProviders asserts that
// the default allowlist contains the providers the matrix marks passthrough ✅,
// so the default handler does not reject those requests before contacting the
// upstream. hetzner was caught by greptile P1 on PR #701; jev is reachable
// only through passthrough, so leaving it out would make the provider inert.
func TestDefaultEnabledPassthroughProvidersIncludesMatrixProviders(t *testing.T) {
	for _, providerType := range []string{"hetzner", "jev"} {
		found := slices.Contains(defaultEnabledPassthroughProviders, providerType)
		require.True(t, found, "defaultEnabledPassthroughProviders = %v, want %s included", defaultEnabledPassthroughProviders, providerType)
	}
}

// TestDefaultEnabledPassthroughProvidersIncludesEdenAI asserts that the default
// allowlist contains edenai — the provider matrix marks edenai passthrough ✅,
// and the default handler must not reject those requests before contacting the
// upstream.
func TestDefaultEnabledPassthroughProvidersIncludesEdenAI(t *testing.T) {
	require.Contains(t, defaultEnabledPassthroughProviders, "edenai", "defaultEnabledPassthroughProviders = %v, want edenai included", defaultEnabledPassthroughProviders)
}

// A successful non-streaming JSON passthrough response must produce a usage
// entry from its usage member — the same accounting SSE streams get from the
// stream usage observer. Covers the /p/{provider} surface directly.
func TestProxyPassthroughNonStreamingLogsUsage(t *testing.T) {
	body := `{"id":"msg_p","type":"message","model":"claude-fable-5","usage":{"input_tokens":42,"output_tokens":6}}`
	resp := &core.PassthroughResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}

	c, rec := echotest.Post(t, "/p/anthropic/messages", `{}`)

	info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages", Model: "claude-fable-5"}
	err := proxyPassthroughResponse(c, nil, usageLogger, nil, "anthropic", "anthropic", "messages", info, resp)
	require.NoError(t, err)
	require.Equal(t, body, rec.Body.String())
	require.Len(t, usageLogger.entries, 1)

	entry := usageLogger.entries[0]
	assert.Equal(t, 42, entry.InputTokens)
	assert.Equal(t, 6, entry.OutputTokens)
	assert.Equal(t, "msg_p", entry.ProviderID)
}

// A stream the client abandons is recorded once, as a client disconnect. The
// error must not escape to the central error handler, which would log the
// same request again as a gateway 500.
func TestProxyPassthroughStreamClientDisconnectIsNotReturned(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	chunk := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	resp := &core.PassthroughResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(io.MultiReader(strings.NewReader(chunk), iotest.ErrReader(context.Canceled))),
	}

	c, rec := echotest.Post(t, "/v1/messages", `{}`)
	ctx, cancel := context.WithCancel(c.Request().Context())
	cancel()
	c.SetRequest(c.Request().WithContext(ctx))

	info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages", Model: "claude-fable-5"}
	require.NoError(t, proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp))
	assert.Equal(t, chunk, rec.Body.String())

	logOutput := logs.String()
	assert.Contains(t, logOutput, `"msg":"stream terminated abnormally"`)
	assert.Contains(t, logOutput, `"error_type":"client_disconnected"`)
	assert.NotContains(t, logOutput, `"level":"ERROR"`)
}

// brokenClientWriter fails every body write the way a closed client
// connection does.
type brokenClientWriter struct {
	http.ResponseWriter
}

func (brokenClientWriter) Write([]byte) (int, error) {
	return 0, &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}
}

// A response that breaks after its status line was sent is recorded on the
// request log entry before the handler returns, and is not returned as an error.
func TestRelayFailuresAreRecordedNotReturned(t *testing.T) {
	jsonHeaders := map[string][]string{"Content-Type": {"application/json"}}
	truncatedBody := func() io.ReadCloser {
		return io.NopCloser(io.MultiReader(strings.NewReader(`{"id":"msg_1",`), iotest.ErrReader(io.ErrUnexpectedEOF)))
	}
	info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages", Model: "claude-fable-5"}

	tests := []struct {
		name          string
		brokenClient  bool
		relay         func(c *echo.Context) error
		wantStatus    int
		wantErrorType string
	}{
		{
			name: "passthrough body cut short by the provider",
			relay: func(c *echo.Context) error {
				resp := &core.PassthroughResponse{StatusCode: http.StatusOK, Headers: jsonHeaders, Body: truncatedBody()}
				return proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp)
			},
			wantStatus:    http.StatusOK,
			wantErrorType: "stream_error",
		},
		{
			name:         "passthrough body the client stopped reading",
			brokenClient: true,
			relay: func(c *echo.Context) error {
				resp := &core.PassthroughResponse{StatusCode: http.StatusOK, Headers: jsonHeaders, Body: io.NopCloser(strings.NewReader(`{"id":"msg_1"}`))}
				return proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp)
			},
			wantStatus:    http.StatusOK,
			wantErrorType: "client_disconnected",
		},
		{
			// The provider's own error is already on the request log entry and stays
			// the recorded cause.
			name:         "relayed provider error the client stopped reading",
			brokenClient: true,
			relay: func(c *echo.Context) error {
				body := `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is required"}}`
				resp := &core.PassthroughResponse{StatusCode: http.StatusBadRequest, Headers: jsonHeaders, Body: io.NopCloser(strings.NewReader(body))}
				return proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp)
			},
			wantStatus:    http.StatusBadRequest,
			wantErrorType: string(core.ErrorTypeInvalidRequest),
		},
		{
			name: "realtime signaling body cut short by the provider",
			relay: func(c *echo.Context) error {
				return relayRealtimeHTTPResponse(c, &http.Response{StatusCode: http.StatusCreated, Body: truncatedBody()}, "application/sdp")
			},
			wantStatus:    http.StatusCreated,
			wantErrorType: "stream_error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opts []echotest.Option
			if tc.brokenClient {
				opts = append(opts, echotest.WithResponseWriter(func(w http.ResponseWriter) http.ResponseWriter {
					return brokenClientWriter{ResponseWriter: w}
				}))
			}
			c, rec := echotest.Post(t, "/v1/messages", `{}`, opts...)
			entry := &requestlog.LogEntry{Data: &requestlog.LogData{}}
			c.Set(string(requestlog.LogEntryKey), entry)

			require.NoError(t, tc.relay(c))

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantErrorType, entry.ErrorType)
		})
	}
}

// Any complete-body success status must be accounted, not only 200: /p/ is
// provider-generic and a 201/202 JSON response can carry usage too.
func TestProxyPassthroughNonStreamingLogsUsageForNon200Success(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusAccepted} {
		body := `{"id":"msg_p","model":"claude-fable-5","usage":{"input_tokens":42,"output_tokens":6}}`
		resp := &core.PassthroughResponse{
			StatusCode: status,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}

		c, rec := echotest.Post(t, "/p/anthropic/messages", `{}`)

		info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages"}
		err := proxyPassthroughResponse(c, nil, usageLogger, nil, "anthropic", "anthropic", "messages", info, resp)
		require.NoError(t, err, "status %d: proxyPassthroughResponse: %v", status, err)
		require.Len(t, usageLogger.entries, 1)
		require.Equal(t, status, rec.Code)
	}
}

// Non-JSON media types and incomplete-body statuses must relay untouched with
// no usage entry: there is nothing trustworthy to account for. The media type
// is parsed, so JSON-adjacent types and parameters must not slip through.
func TestProxyPassthroughNonStreamingSkipsNonAccountableResponses(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
	}{
		{name: "non-JSON content type", status: http.StatusOK, contentType: "application/x-jsonl"},
		{name: "JSON-adjacent media type", status: http.StatusOK, contentType: "application/json-seq"},
		{name: "JSON only in a parameter", status: http.StatusOK, contentType: `text/plain; profile="application/json"`},
		{name: "partial content", status: http.StatusPartialContent, contentType: "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"usage":{"input_tokens":42,"output_tokens":6}}`
			resp := &core.PassthroughResponse{
				StatusCode: tc.status,
				Headers:    map[string][]string{"Content-Type": {tc.contentType}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}
			usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}

			c, rec := echotest.Post(t, "/p/anthropic/messages", `{}`)

			info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages"}
			err := proxyPassthroughResponse(c, nil, usageLogger, nil, "anthropic", "anthropic", "messages", info, resp)
			require.NoError(t, err)
			require.Equal(t, body, rec.Body.String())
			require.Empty(t, usageLogger.entries)
			require.Equal(t, tc.status, rec.Code)
		})
	}
}

// The passthrough contract proxies provider-native error bodies: the Anthropic
// SDK parses {"type":"error",...} and loses its typed errors — and the
// request_id — when the gateway rewrites the body into the OpenAI envelope.
func TestProxyPassthroughRelaysProviderErrorBody(t *testing.T) {
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: Field required"},"request_id":"req_011CexSaBK"}`
	resp := &core.PassthroughResponse{
		StatusCode: http.StatusBadRequest,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
			"X-Upstream":   {"must-not-leak"},
			"Set-Cookie":   {"session=secret"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	c, rec := echotest.Post(t, "/p/anthropic/messages", `{}`)

	info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages"}
	require.NoError(t, proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, body, rec.Body.String(), "error body not relayed verbatim")
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	for _, header := range []string{"X-Upstream", "Set-Cookie"} {
		assert.Empty(t, rec.Header().Get(header), "%s should not be relayed", header)
	}
}

// A provider error body the gateway cannot hand over as-is (an intermediary's
// HTML page, an empty body, an oversized payload) still has to reach the
// client as a parseable gateway error.
func TestProxyPassthroughConvertsUnrelayableErrorBody(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "HTML error page", contentType: "text/html", body: "<html>502</html>"},
		{name: "empty body", contentType: "application/json", body: ""},
		{name: "invalid JSON", contentType: "application/json", body: "{"},
		{name: "oversized body", contentType: "application/json", body: `{"error":"` + strings.Repeat("x", maxRelayedPassthroughErrorBytes) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &core.PassthroughResponse{
				StatusCode: http.StatusBadGateway,
				Headers:    map[string][]string{"Content-Type": {tc.contentType}},
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}

			c, rec := echotest.Post(t, "/p/anthropic/messages", `{}`)

			info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages"}
			require.NoError(t, proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp))
			require.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Contains(t, rec.Body.String(), `"error"`, "body is not a gateway error envelope")
		})
	}
}
