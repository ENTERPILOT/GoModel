package server

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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

// TestDefaultEnabledPassthroughProvidersIncludesHetzner asserts that the default
// allowlist contains hetzner — the provider matrix marks hetzner passthrough ✅,
// and the default handler must not reject those requests before contacting the
// upstream. Caught by greptile P1 on PR #701.
func TestDefaultEnabledPassthroughProvidersIncludesHetzner(t *testing.T) {
	found := slices.Contains(defaultEnabledPassthroughProviders, "hetzner")
	require.True(t, found, "defaultEnabledPassthroughProviders = %v, want hetzner included", defaultEnabledPassthroughProviders)
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

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/anthropic/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages"}
	if err := proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp); err != nil {
		t.Fatalf("proxyPassthroughResponse: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rec.Body.String() != body {
		t.Fatalf("error body not relayed verbatim: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	for _, header := range []string{"X-Upstream", "Set-Cookie"} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s should not be relayed, got %q", header, got)
		}
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

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/p/anthropic/messages", strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			info := &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages"}
			if err := proxyPassthroughResponse(c, nil, nil, nil, "anthropic", "anthropic", "messages", info, resp); err != nil {
				t.Fatalf("proxyPassthroughResponse: %v", err)
			}
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("body is not a gateway error envelope: %s", rec.Body.String())
			}
		})
	}
}
