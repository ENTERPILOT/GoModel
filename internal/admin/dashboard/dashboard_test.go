package dashboard

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

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

// A clean checkout compiles (static/ holds a committed placeholder) but has
// no built dashboard. The constructor must say so rather than serve a broken
// page.
func TestBuildIndexHTML_MissingBuild(t *testing.T) {
	tests := []struct {
		name   string
		assets fstest.MapFS
	}{
		{name: "placeholder only", assets: fstest.MapFS{"static/.gitkeep": {}}},
		{name: "assets without index", assets: fstest.MapFS{"static/dist/assets/index-abc.js": {Data: []byte("//")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildIndexHTML(tt.assets, "/", false)
			if err == nil {
				t.Fatalf("expected error for missing dashboard build")
			}
			if !strings.Contains(err.Error(), "make frontend") {
				t.Errorf("error should tell the user how to build the dashboard, got %q", err)
			}
		})
	}
}

func TestBuildIndexHTML_InjectsGlobalsAndBasePath(t *testing.T) {
	assets := fstest.MapFS{
		"static/dist/index.html": {Data: []byte(`<html><head><script src="/admin/static/assets/index-abc.js"></script></head><body></body></html>`)},
	}
	got, err := buildIndexHTML(assets, "/gateway", true)
	if err != nil {
		t.Fatalf("buildIndexHTML() returned error: %v", err)
	}
	html := string(got)
	for _, want := range []string{
		`src="/gateway/admin/static/assets/index-abc.js"`,
		`window.GOMODEL_BASE_PATH="/gateway"`,
		`window.GOMODEL_DEMO_MODE=true`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q in rendered index.html:\n%s", want, html)
		}
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
