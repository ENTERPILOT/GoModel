package dashboard

import (
	"io/fs"
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

func serveIndex(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.Index(c); err != nil {
		t.Fatalf("Index() returned error: %v", err)
	}
	return rec
}

func serveStatic(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.Static(c); err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}
	return rec
}

func TestIndex_ReturnsHTML(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}

	rec := serveIndex(t, h, "/admin/dashboard")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/html; charset=utf-8" {
		t.Errorf("expected Content-Type text/html; charset=utf-8, got %s", contentType)
	}

	body := rec.Body.String()
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "<!doctype html") && !strings.Contains(lower, "<html") {
		t.Errorf("expected HTML content, got: %.200s", body)
	}
	if !strings.Contains(body, `window.GOMODEL_BASE_PATH="/"`) {
		t.Errorf("expected injected base path global in page HTML")
	}
	if !regexp.MustCompile(`window\.GOMODEL_VERSION="[^"]+"`).MatchString(body) {
		t.Errorf("expected injected version global in page HTML")
	}
	if !strings.Contains(body, "window.GOMODEL_DEMO_MODE=false") {
		t.Errorf("expected demo mode global false in page HTML")
	}
	if !regexp.MustCompile(`/admin/static/assets/index-[^"]+\.js`).MatchString(body) {
		t.Errorf("expected hashed SPA script tag in page HTML")
	}
	if !regexp.MustCompile(`/admin/static/assets/index-[^"]+\.css`).MatchString(body) {
		t.Errorf("expected hashed SPA stylesheet link in page HTML")
	}
}

func TestIndex_DemoModeInjectsFlag(t *testing.T) {
	h, err := NewWithDemoMode("/", true)
	if err != nil {
		t.Fatalf("NewWithDemoMode() returned error: %v", err)
	}
	body := serveIndex(t, h, "/admin/dashboard").Body.String()
	if !strings.Contains(body, "window.GOMODEL_DEMO_MODE=true") {
		t.Error("expected demo mode global true in page HTML")
	}
}

func TestIndex_StandardModeHidesDemoFlag(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	body := serveIndex(t, h, "/admin/dashboard").Body.String()
	if !strings.Contains(body, "window.GOMODEL_DEMO_MODE=false") {
		t.Error("expected demo mode global false in page HTML")
	}
}

func TestIndex_UsesBasePathForGeneratedURLs(t *testing.T) {
	h, err := NewWithBasePath("/gw")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	body := serveIndex(t, h, "/gw/admin/dashboard").Body.String()

	if !strings.Contains(body, `window.GOMODEL_BASE_PATH="/gw"`) {
		t.Errorf("expected injected base path /gw in page HTML")
	}
	if !regexp.MustCompile(`"/gw/admin/static/assets/index-[^"]+\.js`).MatchString(body) {
		t.Errorf("expected base-path-prefixed script URL in page HTML")
	}
	if strings.Contains(body, `"/admin/static/`) {
		t.Errorf("expected no unprefixed asset URLs in page HTML")
	}
}

func TestStatic_ServesSPAAssets(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}

	sub, err := fs.Sub(content, "static/dist")
	if err != nil {
		t.Fatalf("fs.Sub returned error: %v", err)
	}
	entries, err := fs.ReadDir(sub, "assets")
	if err != nil {
		t.Fatalf("expected built assets directory in embed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected built assets in embed, got none")
	}

	for _, entry := range entries {
		rec := serveStatic(t, h, "/admin/static/assets/"+entry.Name())
		if rec.Code != http.StatusOK {
			t.Errorf("asset %s: expected 200, got %d", entry.Name(), rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("asset %s: expected immutable cache header, got %q", entry.Name(), cc)
		}
	}
}

func TestStatic_ServesFavicon(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	rec := serveStatic(t, h, "/admin/static/favicon.svg")
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for favicon, got %d", rec.Code)
	}
}

func TestStatic_ServesFonts(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	rec := serveStatic(t, h, "/admin/static/fonts/inter.css")
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for fonts/inter.css, got %d", rec.Code)
	}
}

func TestStatic_NotFound(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	rec := serveStatic(t, h, "/admin/static/nope.js")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestIndex_HasNoExternalResources keeps the dashboard self-contained: no
// CDN scripts, styles, or fonts.
func TestIndex_HasNoExternalResources(t *testing.T) {
	h, err := NewWithBasePath("/")
	if err != nil {
		t.Fatalf("NewWithBasePath() returned error: %v", err)
	}
	body := serveIndex(t, h, "/admin/dashboard").Body.String()
	for _, marker := range []string{
		"https://cdn.",
		"http://cdn.",
		"unpkg.com",
		"jsdelivr.net",
		"googleapis.com",
	} {
		if strings.Contains(body, marker) {
			t.Errorf("expected no external resource %q in page HTML", marker)
		}
	}
}
