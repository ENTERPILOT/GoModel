package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

// captureSlog routes the default logger into a buffer for the test's duration.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestUnhandledErrorHandler(t *testing.T) {
	type badTime struct {
		At time.Time `json:"at"`
	}
	// A year beyond 9999 makes time.Time.MarshalJSON fail, which is how a
	// stored row can turn a list endpoint into a 500 (#881).
	unmarshalable := badTime{At: time.Unix(1<<40, 0).UTC()}

	tests := []struct {
		name         string
		handler      echo.HandlerFunc
		wantStatus   int
		wantBody     string
		wantLog      []string
		wantEnvelope bool
	}{
		{
			name:         "panic is logged with stack and rendered in the gateway envelope",
			handler:      func(c *echo.Context) error { panic("boom") },
			wantStatus:   http.StatusInternalServerError,
			wantEnvelope: true,
			wantLog:      []string{"unhandled request error", "panic=boom", "stack=", "path=/boom", "status=500"},
		},
		{
			name: "serialization failure is logged and rendered in the gateway envelope",
			handler: func(c *echo.Context) error {
				return c.JSON(http.StatusOK, []badTime{unmarshalable})
			},
			wantStatus:   http.StatusInternalServerError,
			wantEnvelope: true,
			wantLog:      []string{"unhandled request error", "year outside of range", "path=/boom"},
		},
		{
			name:       "echo HTTPError keeps its status and body",
			handler:    func(c *echo.Context) error { return echo.NewHTTPError(http.StatusTeapot, "short and stout") },
			wantStatus: http.StatusTeapot,
			wantBody:   `{"message":"short and stout"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlog(t)
			srv := New(&mockProvider{}, nil)
			srv.echo.GET("/boom", tt.handler)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantBody != "" && strings.TrimSpace(rec.Body.String()) != tt.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantEnvelope {
				var body struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("body %q is not a gateway error envelope: %v", rec.Body.String(), err)
				}
				if body.Error.Type != "internal_error" || body.Error.Message != "an unexpected error occurred" {
					t.Fatalf("error = %+v", body.Error)
				}
				if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "year outside") {
					t.Fatalf("internal error detail leaked into the response: %s", rec.Body.String())
				}
			}
			for _, want := range tt.wantLog {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log output missing %q:\n%s", want, logs.String())
				}
			}
			if len(tt.wantLog) == 0 && strings.Contains(logs.String(), "unhandled request error") {
				t.Errorf("echo HTTPError should not be logged as unhandled:\n%s", logs.String())
			}
		})
	}
}
