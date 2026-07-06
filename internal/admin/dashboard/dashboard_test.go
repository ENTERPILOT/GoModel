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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	if h == nil {
		t.Fatalf("NewWithBasePath() returned nil handler")
	}
}

func TestIndex_ReturnsHTML(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	if !regexp.MustCompile(`/admin/static/js/dashboard\.js\?v=[0-9a-f]+`).MatchString(rec.Body.String()) {
		t.Errorf("expected versioned dashboard JS link in page HTML")
	}
	if !regexp.MustCompile(`/admin/static/js/modules/virtual-models\.js\?v=[0-9a-f]+`).MatchString(rec.Body.String()) {
		t.Errorf("expected versioned dashboard module JS link in page HTML")
	}
	if !strings.Contains(body, "settings-version-footer") {
		t.Errorf("expected settings-version-footer element in page HTML")
	}
	if !strings.Contains(body, "gomodel ") {
		t.Errorf("expected gomodel version string in page HTML")
	}
}

func TestIndex_UsesBasePathForGeneratedURLs(t *testing.T) {
	h, err := NewWithBasePath("g/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Index(c); err != nil {
		t.Fatalf("Index() returned error: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `window.GOMODEL_BASE_PATH = basePath`) ||
		!regexp.MustCompile(`const basePath = "\\?/g";`).MatchString(body) {
		t.Errorf("expected base path bootstrap in page HTML")
	}
	if !regexp.MustCompile(`/g/admin/static/css/dashboard\.css\?v=[0-9a-f]+`).MatchString(body) {
		t.Errorf("expected versioned dashboard CSS link to include base path")
	}
	if !regexp.MustCompile(`/g/admin/static/js/dashboard\.js\?v=[0-9a-f]+`).MatchString(body) {
		t.Errorf("expected versioned dashboard JS link to include base path")
	}
	if !regexp.MustCompile(`/g/admin/static/js/modules/virtual-models\.js\?v=[0-9a-f]+`).MatchString(body) {
		t.Errorf("expected versioned dashboard module JS link to include base path")
	}
	if !strings.Contains(body, `href="/g/admin/dashboard/overview"`) {
		t.Errorf("expected dashboard navigation links to include base path")
	}
	if strings.Contains(body, `href="/admin/dashboard/overview"`) {
		t.Errorf("expected dashboard navigation links not to point at root admin path")
	}
}

func TestStatic_ServesCSS(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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

func TestStatic_ServesVirtualModelsModuleJS(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/static/js/modules/virtual-models.js", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body for virtual-models module JS file")
	}
}

func TestStatic_ServesWorkflowsModuleJS(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
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

// TestIndex_HasNoExternalResources guards the offline guarantee: the rendered
// page must load every script, style, and font from the embedded /admin/static
// tree, never from a CDN or remote font host.
func TestIndex_HasNoExternalResources(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Index(c); err != nil {
		t.Fatalf("Index() returned error: %v", err)
	}

	// Match resources the browser actually fetches (src=, href=, CSS url()),
	// including protocol-relative (//cdn...) URLs, while ignoring inline-SVG
	// namespace URIs like http://www.w3.org/2000/svg.
	loaded := regexp.MustCompile(`(?:src|href)=["'](?:https?:)?//|url\(\s*["']?(?:https?:)?//`)
	if matches := loaded.FindAllString(rec.Body.String(), -1); len(matches) > 0 {
		t.Errorf("expected no external (http/https) resources in page HTML, found: %v", matches)
	}
	for _, want := range []string{
		`/admin/static/fonts/inter.css`,
		`/admin/static/vendor/chart.umd.min.js`,
		`/admin/static/vendor/alpine.min.js`,
		`/admin/static/vendor/lucide.min.js`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("expected local asset %q in page HTML", want)
		}
	}
}

// TestStatic_ServesVendoredAssets confirms the vendored libraries and font
// files are embedded and served, so the dashboard renders without network access.
func TestStatic_ServesVendoredAssets(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}

	paths := []string{
		"/admin/static/vendor/chart.umd.min.js",
		"/admin/static/vendor/alpine.min.js",
		"/admin/static/vendor/lucide.min.js",
		"/admin/static/fonts/inter.css",
		"/admin/static/fonts/inter-latin.woff2",
		"/admin/static/fonts/inter-latin-ext.woff2",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.Static(c); err != nil {
				t.Fatalf("Static() returned error: %v", err)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Errorf("expected non-empty body for %s", path)
			}
		})
	}
}
