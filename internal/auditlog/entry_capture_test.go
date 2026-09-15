package auditlog

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestCaptureInternalJSONExchange_PreservesHeadersWithoutBodies(t *testing.T) {
	entry := &LogEntry{
		RequestID: "req_123",
		Data:      &LogData{},
	}
	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{
			"Traceparent": {`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00`},
		},
		"application/json",
		nil,
		false,
		"req_123",
		nil,
		"/team/alpha",
	))

	CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", nil, nil, nil, Config{
		LogHeaders: true,
		LogBodies:  false,
	})

	require.NotNil(t, entry.Data)
	got := entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")]
	require.Equal(t, "req_123", got)
	require.Equal(t, "/team/alpha", entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)])
	got = entry.Data.RequestHeaders["Traceparent"]
	require.NotEmpty(t, got)
	require.Equal(t, "req_123", entry.Data.ResponseHeaders[http.CanonicalHeaderKey("X-Request-ID")])
	require.Nil(t, entry.Data.RequestBody)
	require.Nil(t, entry.Data.ResponseBody)
}

func TestCaptureInternalJSONExchange_PreservesHeadersWhenBodyMarshalFails(t *testing.T) {
	t.Run("marshal failure preserves headers", func(t *testing.T) {
		entry := &LogEntry{
			RequestID: "req_456",
			Data:      &LogData{},
		}
		ctx := core.WithEffectiveUserPath(context.Background(), "/team/beta")

		CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", func() {}, func() {}, nil, Config{
			LogHeaders: true,
			LogBodies:  true,
		})

		require.NotNil(t, entry.Data)
		require.Equal(t, "req_456", entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")])
		require.Equal(t, "/team/beta", entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)])
		require.Equal(t, "req_456", entry.Data.ResponseHeaders[http.CanonicalHeaderKey("X-Request-ID")])
		require.Nil(t, entry.Data.RequestBody)
		require.Nil(t, entry.Data.ResponseBody)
	})

	t.Run("response error preserves headers and captures error body", func(t *testing.T) {
		entry := &LogEntry{
			RequestID: "req_456_err",
			Data:      &LogData{},
		}
		ctx := core.WithEffectiveUserPath(context.Background(), "/team/beta")
		responseErr := core.NewProviderError("openai", http.StatusBadGateway, "upstream failed", fmt.Errorf("boom"))

		CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", map[string]any{"ok": true}, nil, responseErr, Config{
			LogHeaders: true,
			LogBodies:  true,
		})

		require.NotNil(t, entry.Data)
		require.Equal(t, "req_456_err", entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")])
		require.Equal(t, "/team/beta", entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)])
		require.Equal(t, "req_456_err", entry.Data.ResponseHeaders[http.CanonicalHeaderKey("X-Request-ID")])
		require.Equal(t, "/team/beta", entry.Data.ResponseHeaders[http.CanonicalHeaderKey(core.UserPathHeader)])

		body, ok := BodyDocument(entry.Data.ResponseBody).(map[string]any)
		require.True(t, ok, "ResponseBody = %T, want synthesized error envelope", entry.Data.ResponseBody)

		errorBody, ok := body["error"].(map[string]any)
		require.True(t, ok, "ResponseBody[error] = %#v, want object", body["error"])

		require.Equal(t, "upstream failed", errorBody["message"])
		require.Equal(t, string(core.ErrorTypeProvider), errorBody["type"])
		require.Contains(t, errorBody, "param")
		require.Nil(t, errorBody["param"])
		require.Contains(t, errorBody, "code")
		require.Nil(t, errorBody["code"])
	})

	t.Run("response error takes precedence over body payload", func(t *testing.T) {
		entry := &LogEntry{
			RequestID: "req_456_err_body",
			Data:      &LogData{},
		}
		ctx := core.WithEffectiveUserPath(context.Background(), "/team/beta")
		responseErr := core.NewProviderError("openai", http.StatusBadGateway, "upstream failed", fmt.Errorf("boom"))

		CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", map[string]any{"ok": true}, map[string]any{"status": "completed"}, responseErr, Config{
			LogHeaders: true,
			LogBodies:  true,
		})

		body, ok := BodyDocument(entry.Data.ResponseBody).(map[string]any)
		require.True(t, ok, "ResponseBody = %T, want synthesized error envelope", entry.Data.ResponseBody)

		errorBody, ok := body["error"].(map[string]any)
		require.True(t, ok, "ResponseBody[error] = %#v, want object", body["error"])
		require.Equal(t, "upstream failed", errorBody["message"])
	})

	t.Run("oversized payload preserves headers and sets truncation flags", func(t *testing.T) {
		entry := &LogEntry{
			RequestID: "req_456_big",
			Data:      &LogData{},
		}
		ctx := core.WithEffectiveUserPath(context.Background(), "/team/beta")
		large := strings.Repeat("x", int(MaxBodyCapture)+1024)

		CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions",
			map[string]any{"payload": large},
			map[string]any{"payload": large},
			nil,
			Config{
				LogHeaders: true,
				LogBodies:  true,
			},
		)

		require.NotNil(t, entry.Data)
		require.Equal(t, "req_456_big", entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")])
		require.Equal(t, "req_456_big", entry.Data.ResponseHeaders[http.CanonicalHeaderKey("X-Request-ID")])
		require.Equal(t, "/team/beta", entry.Data.ResponseHeaders[http.CanonicalHeaderKey(core.UserPathHeader)])
		require.True(t, entry.Data.RequestBodyTooBigToHandle)
		require.Nil(t, entry.Data.RequestBody)
		require.True(t, entry.Data.ResponseBodyTooBigToHandle)

		responseBody, ok := entry.Data.ResponseBody.(string)
		require.True(t, ok, "ResponseBody = %T, want truncated string payload", entry.Data.ResponseBody)
		require.NotEmpty(t, responseBody)
		require.False(t, strings.Contains(responseBody, `"`+large+`"`))
	})
}

func TestCaptureInternalJSONExchange_DoesNotReuseIngressSnapshotOnMarshalFailure(t *testing.T) {
	entry := &LogEntry{
		RequestID: "req_789",
		Data:      &LogData{},
	}
	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{
			"Traceparent": {`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00`},
		},
		"application/json",
		[]byte(`{"outer":"body"}`),
		false,
		"req_outer",
		nil,
		"/team/outer",
	))
	ctx = core.WithEffectiveUserPath(ctx, "/team/internal")

	CaptureInternalJSONExchange(entry, ctx, "POST", "/v1/chat/completions", func() {}, nil, nil, Config{
		LogHeaders: true,
		LogBodies:  true,
	})

	require.NotNil(t, entry.Data)
	require.Nil(t, entry.Data.RequestBody)
	require.False(t, entry.Data.RequestBodyTooBigToHandle)
	require.Equal(t, "req_789", entry.Data.RequestHeaders[http.CanonicalHeaderKey("X-Request-ID")])
	require.Equal(t, "/team/internal", entry.Data.RequestHeaders[http.CanonicalHeaderKey(core.UserPathHeader)])
}
