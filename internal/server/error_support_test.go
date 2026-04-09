package server

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

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
