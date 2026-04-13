package dashboard

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestNew(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if h == nil {
		t.Fatal("New() returned nil handler")
	}
}

func TestIndex_ReturnsHTML(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Index(c); err != nil {
		t.Fatalf("Index() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/html; charset=utf-8" {
		t.Errorf("expected Content-Type text/html; charset=utf-8, got %s", contentType)
	}

	body := strings.ToLower(rec.Body.String())
	if !strings.Contains(body, "<!doctype html") && !strings.Contains(body, "<html") {
		t.Errorf("expected HTML content, got: %.200s", rec.Body.String())
	}
	if !strings.Contains(body, "audit logs") {
		t.Errorf("expected audit logs navigation item in page HTML")
	}
	if !strings.Contains(body, "workflows") {
		t.Errorf("expected workflows navigation item in page HTML")
	}
	if !strings.Contains(body, `x-data="dashboard()"`) {
		t.Errorf("expected alpine dashboard root in page HTML")
	}
	if strings.Contains(body, `x-init="init()"`) {
		t.Errorf("expected dashboard HTML not to call init() explicitly")
	}
	if !regexp.MustCompile(`/admin/static/css/dashboard\.css\?v=[0-9a-f]+`).MatchString(rec.Body.String()) {
		t.Errorf("expected versioned dashboard CSS link in page HTML")
	}
}

func TestStatic_ServesCSS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/css/dashboard.css", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for CSS file")
	}
}

func TestStatic_ServesJS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/dashboard.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for JS file")
	}
}

func TestStatic_ServesModuleJS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/modules/usage.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for module JS file")
	}
}

func TestStatic_ServesProvidersModuleJS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/modules/providers.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for providers module JS file")
	}
}

func TestStatic_ServesAliasesModuleJS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/modules/aliases.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for aliases module JS file")
	}
}

func TestStatic_ServesWorkflowsModuleJS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/modules/workflows.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for workflows module JS file")
	}
}

func TestStatic_ServesGuardrailsModuleJS(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/modules/guardrails.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for guardrails module JS file")
	}
}

func TestStatic_ServesFavicon(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/favicon.svg", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for favicon")
	}
}

func TestStatic_NotFound(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/nonexistent.txt", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}
