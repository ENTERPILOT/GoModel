package opencodego

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/version"
)

const (
	// sessionHeader is the header OpenCode Zen uses to route every request of
	// one conversation to the same upstream so its prompt cache stays warm.
	// Upstream documents it as required for third-party clients.
	sessionHeader = "x-opencode-session"
	// clientHeader identifies the calling tool to OpenCode Zen. Upstream asks
	// clients to identify themselves instead of using generic user agents.
	clientHeader = "x-opencode-client"
	// defaultClient is sent when the inbound request carries no client name.
	defaultClient = "gomodel"
)

// sessionHeaderEnvVar toggles forwarding of x-opencode-session. It defaults to
// enabled; set it to false to leave the header out.
const sessionHeaderEnvVar = "OPENCODE_GO_SESSION_HEADER"

// loadSessionHeaderEnabled reports whether x-opencode-session should be sent,
// honoring OPENCODE_GO_SESSION_HEADER. Unset, empty, or unparsable values keep
// the default of true.
func loadSessionHeaderEnabled() bool {
	raw, configured := os.LookupEnv(sessionHeaderEnvVar)
	if !configured || strings.TrimSpace(raw) == "" {
		return true
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("invalid OpenCode Go session header flag; using default",
			"env", sessionHeaderEnvVar,
			"value", raw,
			"default", true)
		return true
	}
	return enabled
}

// requestHeaders returns the per-request identification headers OpenCode Zen
// expects from a gateway: x-opencode-session (when enabled and a session is
// known), x-opencode-client, and a non-generic User-Agent. The same set is
// applied on both the /chat/completions and /messages paths.
func requestHeaders(sessionEnabled bool) func(context.Context) http.Header {
	userAgent := "gomodel/" + version.Version
	return func(ctx context.Context) http.Header {
		headers := make(http.Header, 3)
		headers.Set("User-Agent", userAgent)
		client := inboundHeader(ctx, clientHeader)
		if client == "" {
			client = defaultClient
		}
		headers.Set(clientHeader, client)
		if sessionEnabled {
			if id := sessionID(ctx); id != "" {
				headers.Set(sessionHeader, id)
			}
		}
		return headers
	}
}

// chatRequestHeaders adapts requestHeaders to the ChatRequestHeaders hook
// signature of the OpenAI-compatible provider.
func chatRequestHeaders(fn func(context.Context) http.Header) func(context.Context, *core.ChatRequest) http.Header {
	return func(ctx context.Context, _ *core.ChatRequest) http.Header {
		return fn(ctx)
	}
}

// sessionID resolves the value for x-opencode-session. A client that already
// speaks OpenCode Zen's dialect (OpenCode itself, pi, …) sends the header and
// its value is forwarded verbatim; otherwise the session id GoModel detected
// (X-Session-Id, Claude Code's session, a body field, or the content-derived
// auto id) stands in, so any coding agent gets cache affinity for free.
func sessionID(ctx context.Context) string {
	if id := inboundHeader(ctx, sessionHeader); id != "" {
		return id
	}
	return core.SessionIDFromContext(ctx)
}

// inboundHeader returns the first usable value of name on the captured
// inbound request, or "" when absent. Values carrying line breaks are
// ignored so a client cannot inject headers upstream.
func inboundHeader(ctx context.Context, name string) string {
	snapshot := core.GetRequestSnapshot(ctx)
	if snapshot == nil {
		return ""
	}
	for key, values := range snapshot.HeadersView() {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" && !strings.ContainsAny(value, "\r\n") {
				return value
			}
		}
	}
	return ""
}
