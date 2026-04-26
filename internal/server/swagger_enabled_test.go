//go:build swagger

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSwaggerEndpoint_Enabled(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{SwaggerEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected text/html Content-Type, got %s", contentType)
	}

	if !strings.Contains(rec.Body.String(), "swagger") {
		t.Errorf("expected body to contain swagger UI content, got: %s", rec.Body.String()[:min(200, len(rec.Body.String()))])
	}
}

func TestSwaggerDocJson_ReturnsExpectedContent(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{SwaggerEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "GoModel") {
		t.Errorf("expected doc.json to contain GoModel API title, got: %s", body[:min(300, len(body))])
	}
	if !strings.Contains(body, "swagger") {
		t.Errorf("expected doc.json to contain swagger spec, got: %s", body[:min(300, len(body))])
	}
}
