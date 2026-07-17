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
