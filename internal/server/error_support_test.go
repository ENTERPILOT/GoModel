package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
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
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			_ = handleError(c, core.NewInvalidRequestError("bad input", nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// Anthropic envelope: {"type":"error","error":{...}}.
			// OpenAI envelope:    {"error":{...}} with no top-level "type".
			if tc.wantAnthropic {
				if body["type"] != "error" {
					t.Errorf("expected Anthropic envelope, got %v", body)
				}
				errObj, _ := body["error"].(map[string]any)
				if errObj["type"] != "invalid_request_error" {
					t.Errorf("error.type = %v", errObj["type"])
				}
			} else {
				if _, hasType := body["type"]; hasType {
					t.Errorf("expected OpenAI envelope without top-level type, got %v", body)
				}
				if _, hasErr := body["error"]; !hasErr {
					t.Errorf("expected OpenAI error envelope, got %v", body)
				}
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

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithRequestID(req.Context(), "warn-req-123"))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handleError(c, core.NewInvalidRequestError("unsupported model: nope", nil)); err != nil {
		t.Fatalf("handleError() error = %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, `"level":"WARN"`) {
		t.Fatalf("expected WARN log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `"msg":"request failed"`) {
		t.Fatalf("expected request failed log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `"request_id":"warn-req-123"`) {
		t.Fatalf("expected request_id in log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `"message":"unsupported model: nope"`) {
		t.Fatalf("expected error message in log, got %q", logOutput)
	}
}

func TestHandleError_LogsServerErrorsAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithRequestID(req.Context(), "error-req-456"))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	upstreamErr := errors.New("upstream timed out")
	if err := handleError(c, core.NewProviderError("openai", http.StatusGatewayTimeout, "provider timeout", upstreamErr)); err != nil {
		t.Fatalf("handleError() error = %v", err)
	}

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, `"level":"ERROR"`) {
		t.Fatalf("expected ERROR log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `"provider":"openai"`) {
		t.Fatalf("expected provider in log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `"request_id":"error-req-456"`) {
		t.Fatalf("expected request_id in log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `"message":"provider timeout"`) {
		t.Fatalf("expected error message in log, got %q", logOutput)
	}
}

func TestHandleError_EnrichesAuditEntryWithGatewayErrorCode(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	err := core.NewRateLimitError("budget", "budget exceeded").WithCode("budget_exceeded")
	if handleErr := handleError(c, err); handleErr != nil {
		t.Fatalf("handleError() error = %v", handleErr)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if entry.ErrorType != string(core.ErrorTypeRateLimit) {
		t.Fatalf("entry.ErrorType = %q, want %q", entry.ErrorType, core.ErrorTypeRateLimit)
	}
	if entry.Data.ErrorMessage != "budget exceeded" {
		t.Fatalf("entry.Data.ErrorMessage = %q, want budget exceeded", entry.Data.ErrorMessage)
	}
	if entry.Data.ErrorCode != "budget_exceeded" {
		t.Fatalf("entry.Data.ErrorCode = %q, want budget_exceeded", entry.Data.ErrorCode)
	}
}

func TestHandleRouteNotFound_AnthropicDialect(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/batches", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handleRouteNotFound(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Type != "error" || body.Error.Type != "not_found_error" {
		t.Errorf("envelope = %+v, want anthropic error envelope", body)
	}
	if !strings.Contains(body.Error.Message, "/v1/messages/batches") {
		t.Errorf("message should name the path, got %q", body.Error.Message)
	}
}

func TestHandleRouteNotFound_OpenAIDialect(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/does-not-exist", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handleRouteNotFound(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error.Type != "not_found_error" {
		t.Errorf("envelope = %s, want OpenAI error envelope with not_found_error", rec.Body.String())
	}
}

func TestHandleError_RecordsUpstreamProviderOfError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	upstream := core.ParseProviderError("openai", http.StatusUnauthorized, []byte(`{"error":{"message":"Incorrect API key provided"}}`), nil)
	if handleErr := handleError(c, upstream); handleErr != nil {
		t.Fatalf("handleError() error = %v", handleErr)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if entry.ErrorType != string(core.ErrorTypeAuthentication) {
		t.Fatalf("entry.ErrorType = %q, want %q", entry.ErrorType, core.ErrorTypeAuthentication)
	}
	if entry.Data.ErrorProvider != "openai" {
		t.Fatalf("entry.Data.ErrorProvider = %q, want openai", entry.Data.ErrorProvider)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errorData, _ := body["error"].(map[string]any)
	if errorData["provider"] != "openai" {
		t.Fatalf("body.error.provider = %v, want openai", errorData["provider"])
	}

	// The gateway's own authentication failure names no provider.
	rec = httptest.NewRecorder()
	c = e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), rec)
	entry = &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)
	if handleErr := handleError(c, core.NewAuthenticationError("", "invalid API key")); handleErr != nil {
		t.Fatalf("handleError() error = %v", handleErr)
	}
	if entry.Data.ErrorProvider != "" {
		t.Fatalf("entry.Data.ErrorProvider = %q, want empty", entry.Data.ErrorProvider)
	}
	if strings.Contains(rec.Body.String(), `"provider"`) {
		t.Fatalf("gateway auth error body should omit provider: %s", rec.Body.String())
	}
}

func TestGatewayErrorHandler_RendersCanonicalEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
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
			name:       "gateway error passes through",
			err:        core.NewNotFoundError("no such model"),
			wantStatus: http.StatusNotFound,
			wantType:   "not_found_error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/admin/virtual-models", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			gatewayErrorHandler(c, tc.err)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
			}
			if body.Error.Type != tc.wantType {
				t.Errorf("error type = %q, want %q (body %s)", body.Error.Type, tc.wantType, rec.Body.String())
			}
			if body.Error.Message == "" {
				t.Errorf("error message is empty, body %s", rec.Body.String())
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

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"internal_error"`) {
		t.Errorf("body = %s, want the canonical error envelope", rec.Body.String())
	}

	logOutput := buf.String()
	for _, want := range []string{`"level":"ERROR"`, `"path":"/admin/virtual-models"`, `"request_id":"panic-req-1"`, "listing blew up", "PANIC RECOVER"} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log missing %q, got %q", want, logOutput)
		}
	}
}

func TestGatewayErrorHandler_LeavesCommittedResponseAlone(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := c.String(http.StatusOK, "streamed"); err != nil {
		t.Fatalf("String() error = %v", err)
	}
	gatewayErrorHandler(c, errors.New("failed after the first chunk"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (response already committed)", rec.Code)
	}
	if rec.Body.String() != "streamed" {
		t.Fatalf("body = %q, want the already-written body", rec.Body.String())
	}
}
