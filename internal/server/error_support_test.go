package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestHandleError_RendersDialectSpecificEnvelope(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		wantAnthropic bool
	}{
		{name: "anthropic dialect", path: "/v1/messages", wantAnthropic: true},
		{name: "anthropic count_tokens", path: "/v1/messages/count_tokens", wantAnthropic: true},
		{name: "openai dialect", path: "/v1/chat/completions", wantAnthropic: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := echotest.Post(t, tc.path, nil)

			_ = handleError(c, core.NewInvalidRequestError("bad input", nil))

			require.Equal(t, http.StatusBadRequest, rec.Code)

			body := echotest.Decode[map[string]any](t, rec)

			// Anthropic envelope: {"type":"error","error":{...}}.
			// OpenAI envelope:    {"error":{...}} with no top-level "type".
			if tc.wantAnthropic {
				assert.Equal(t, "error", body["type"], "expected Anthropic envelope, got %v", body)

				errObj, _ := body["error"].(map[string]any)
				assert.Equal(t, "invalid_request_error", errObj["type"])
			} else {
				_, hasType := body["type"]
				assert.False(t, hasType, "expected OpenAI envelope without top-level type, got %v", body)
				_, hasErr := body["error"]
				assert.True(t, hasErr, "expected OpenAI error envelope, got %v", body)
			}
		})
	}
}

func TestHandleError_LogsClientErrorsAtWarnLevel(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), "warn-req-123")))
	err := handleError(c, core.NewInvalidRequestError("unsupported model: nope", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	logOutput := buf.String()
	require.Contains(t, logOutput, `"level":"WARN"`)
	require.Contains(t, logOutput, `"msg":"request failed"`)
	require.Contains(t, logOutput, `"request_id":"warn-req-123"`)
	require.Contains(t, logOutput, `"message":"unsupported model: nope"`)
}

func TestHandleError_LogsServerErrorsAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestID(c.Request().Context(), "error-req-456")))

	upstreamErr := errors.New("upstream timed out")
	err := handleError(c, core.NewProviderError("openai", http.StatusGatewayTimeout, "provider timeout", upstreamErr))
	require.NoError(t, err)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)

	logOutput := buf.String()
	require.Contains(t, logOutput, `"level":"ERROR"`)
	require.Contains(t, logOutput, `"provider":"openai"`)
	require.Contains(t, logOutput, `"request_id":"error-req-456"`)
	require.Contains(t, logOutput, `"message":"provider timeout"`)
}

func TestHandleError_EnrichesAuditEntryWithGatewayErrorCode(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	err := core.NewRateLimitError("budget", "budget exceeded").WithCode("budget_exceeded")
	handleErr := handleError(c, err)
	require.NoError(t, handleErr)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, string(core.ErrorTypeRateLimit), entry.ErrorType)
	require.Equal(t, "budget exceeded", entry.Data.ErrorMessage)
	require.Equal(t, "budget_exceeded", entry.Data.ErrorCode)
}

func TestHandleRouteNotFound_AnthropicDialect(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/messages/batches", nil, echotest.WithHeader("anthropic-version", "2023-06-01"))
	err := handleRouteNotFound(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)

	body := echotest.Decode[struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}](t, rec)
	assert.Equal(t, "error", body.Type)
	assert.Equal(t, "not_found_error", body.Error.Type, "envelope = %+v, want anthropic error envelope", body)
	assert.Contains(t, body.Error.Message, "/v1/messages/batches")
}

func TestHandleRouteNotFound_OpenAIDialect(t *testing.T) {
	c, rec := echotest.Get(t, "/v1/does-not-exist")
	err := handleRouteNotFound(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)

	body := echotest.Decode[struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}](t, rec)
	assert.Equal(t, "not_found_error", body.Error.Type, "envelope = %s, want OpenAI error envelope with not_found_error", rec.Body.String())
}

func TestHandleError_RecordsUpstreamProviderOfError(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	upstream := core.ParseProviderError("openai", http.StatusUnauthorized, []byte(`{"error":{"message":"Incorrect API key provided"}}`), nil)
	handleErr := handleError(c, upstream)
	require.NoError(t, handleErr)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, string(core.ErrorTypeAuthentication), entry.ErrorType)
	require.Equal(t, "openai", entry.Data.ErrorProvider)

	body := echotest.Decode[map[string]any](t, rec)

	errorData, _ := body["error"].(map[string]any)
	require.Equal(t, "openai", errorData["provider"])

	// The gateway's own authentication failure names no provider.
	c, rec = echotest.Post(t, "/v1/chat/completions", nil)
	entry = &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	handleErr = handleError(c, core.NewAuthenticationError("", "invalid API key"))
	require.NoError(t, handleErr)
	require.Empty(t, entry.Data.ErrorProvider)
	assert.NotContains(t, rec.Body.String(), `"provider"`)
}

func TestGatewayErrorHandler_RendersCanonicalEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantType    string
		wantMessage string
	}{
		{
			name:       "recovered panic",
			err:        errors.New("runtime error: invalid memory address"),
			wantStatus: http.StatusInternalServerError,
			wantType:   "internal_error",
		},
		{
			name:       "unencodable response body",
			err:        &json.UnsupportedValueError{Str: "+Inf"},
			wantStatus: http.StatusInternalServerError,
			wantType:   "internal_error",
		},
		{
			name:       "echo client error keeps its status",
			err:        echo.NewHTTPError(http.StatusMethodNotAllowed, "Method Not Allowed"),
			wantStatus: http.StatusMethodNotAllowed,
			wantType:   "invalid_request_error",
		},
		{
			name:       "middleware sentinel keeps its status",
			err:        echo.ErrStatusRequestEntityTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "invalid_request_error",
		},
		{
			// echo's Decompress middleware answers 500 with err.Error(); a
			// message describing gateway internals must not reach the client.
			name:        "echo server error stays opaque",
			err:         echo.NewHTTPError(http.StatusInternalServerError, "dial tcp 10.0.0.1:5432: connection refused"),
			wantStatus:  http.StatusInternalServerError,
			wantType:    "internal_error",
			wantMessage: "an unexpected error occurred",
		},
		{
			name:       "gateway error passes through",
			err:        core.NewNotFoundError("no such model"),
			wantStatus: http.StatusNotFound,
			wantType:   "not_found_error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := echotest.Get(t, "/admin/virtual-models")

			gatewayErrorHandler(c, tc.err)

			require.Equal(t, tc.wantStatus, rec.Code)

			body := echotest.Decode[struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}](t, rec)
			assert.Equal(t, tc.wantType, body.Error.Type, "error type = %q, want %q (body %s)", body.Error.Type, tc.wantType, rec.Body.String())
			assert.NotEmpty(t, body.Error.Message, "error message is empty, body %s", rec.Body.String())

			if tc.wantMessage != "" {
				assert.Equal(t, tc.wantMessage, body.Error.Message)
			}
		})
	}
}

func TestGatewayErrorHandler_LogsPanicWithStackAndRoute(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })

	e := echo.NewWithConfig(echo.Config{HTTPErrorHandler: gatewayErrorHandler})
	e.Use(middleware.Recover())
	e.GET("/admin/virtual-models", func(*echo.Context) error {
		panic("listing blew up")
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/virtual-models", nil)
	req = req.WithContext(core.WithRequestID(req.Context(), "panic-req-1"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"internal_error"`)

	logOutput := buf.String()
	for _, want := range []string{`"level":"ERROR"`, `"path":"/admin/virtual-models"`, `"request_id":"panic-req-1"`, `"panic":"listing blew up"`, `"stack":"goroutine `} {
		assert.Contains(t, logOutput, want)
	}
	assert.NotContains(t, logOutput, "PANIC RECOVER", "panic value and stack should be separate attributes, not one error string")
}

func TestGatewayErrorHandler_LeavesCommittedResponseAlone(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	c, rec := echotest.Get(t, "/v1/chat/completions")
	err := c.String(http.StatusOK, "streamed")
	require.NoError(t, err)

	gatewayErrorHandler(c, errors.New("failed after the first chunk"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "streamed", rec.Body.String())

	// The response is untouchable, but the operator still needs to know.
	require.Contains(t, buf.String(), "failed after the first chunk")
}

// The direct gatewayErrorHandler tests stub the error; this one drives the real
// server so the goccy serializer, echo's dispatch and the handler are all in
// the path — the shape that produced the opaque 500 in #881.
func TestGatewayErrorHandler_UnencodableResponseThroughRealServer(t *testing.T) {
	logs := &bytes.Buffer{}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })

	// A year beyond 9999 is what time.Time refuses to marshal, which is how one
	// stored row turned a whole admin listing into a 500.
	type row struct {
		CreatedAt time.Time `json:"created_at"`
	}
	srv := New(&mockProvider{}, nil)
	srv.echo.GET("/admin/rows", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, []row{{CreatedAt: time.Unix(1<<40, 0).UTC()}})
	})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/rows", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	body := echotest.Decode[struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}](t, rec)
	require.Equal(t, "internal_error", body.Error.Type)
	require.Equal(t, "an unexpected error occurred", body.Error.Message, "error = %+v, want the canonical internal_error envelope", body.Error)
	assert.NotContains(t, rec.Body.String(), "year outside", "serializer detail leaked to the client")
	assert.Contains(t, logs.String(), "year outside of range")
}

func TestGatewayErrorHandler_FinalizesHeadersAndAudit(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	gatewayErrorHandler(c, &gatewayErrorWithResponseHeaders{
		GatewayError: core.NewRateLimitError("budget", "budget exceeded").WithCode("budget_exceeded"),
		headers:      http.Header{"Retry-After": []string{"30"}},
	})

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "30", rec.Header().Get("Retry-After"))
	assert.Equal(t, string(core.ErrorTypeRateLimit), entry.ErrorType)
	assert.Equal(t, "budget_exceeded", entry.Data.ErrorCode)
}

func TestGatewayErrorHandler_KeepsTheOriginalAuditedError(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	// handleError records the real cause and returns its own write failure,
	// which then escapes here; that follow-up must not overwrite the cause.
	err := handleError(c, core.NewNotFoundError("no such model"))
	require.NoError(t, err)

	gatewayErrorHandler(c, errors.New("writing the error response failed"))

	assert.Equal(t, string(core.ErrorTypeNotFound), entry.ErrorType)
	assert.Equal(t, "no such model", entry.Data.ErrorMessage)
}
