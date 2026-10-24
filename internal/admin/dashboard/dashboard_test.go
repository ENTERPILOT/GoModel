package dashboard

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			require.Error(t, err)
			assert.Contains(t, err.Error(), "make frontend")
		})
	}
}

func TestBuildIndexHTML_InjectsGlobalsAndBasePath(t *testing.T) {
	assets := fstest.MapFS{
		"static/dist/index.html": {Data: []byte(`<html><head><script src="/admin/static/assets/index-abc.js"></script></head><body></body></html>`)},
	}
	got, err := buildIndexHTML(assets, "/gateway", true)
	require.NoError(t, err)

	html := string(got)
	for _, want := range []string{
		`src="/gateway/admin/static/assets/index-abc.js"`,
		`window.GOMODEL_BASE_PATH="/gateway"`,
		`window.GOMODEL_DEMO_MODE=true`,
	} {
		assert.Contains(t, html, want)
	}
}

func serveIndex(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Get(t, target)
	require.NoError(t, h.Index(c))
	return rec
}

func serveStatic(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Get(t, target)
	require.NoError(t, h.Static(c))
	return rec
}

func TestIndex_ReturnsHTML(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	rec := serveIndex(t, h, "/admin/dashboard")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))

	body := rec.Body.String()
	lower := strings.ToLower(body)
	assert.True(t, strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html"), "expected HTML content, got: %.200s", body)
	assert.Contains(t, body, `window.GOMODEL_BASE_PATH="/"`)
	assert.Regexp(t, `window\.GOMODEL_VERSION="[^"]+"`, body)
	assert.Contains(t, body, "window.GOMODEL_DEMO_MODE=false")
	assert.Regexp(t, `/admin/static/assets/index-[^"]+\.js`, body)
	assert.Regexp(t, `/admin/static/assets/index-[^"]+\.css`, body)
}

func TestIndex_DemoModeInjectsFlag(t *testing.T) {
	h, err := NewWithDemoMode("/", true)
	require.NoError(t, err)

	body := serveIndex(t, h, "/admin/dashboard").Body.String()
	assert.Contains(t, body, "window.GOMODEL_DEMO_MODE=true")
}

func TestIndex_UsesBasePathForGeneratedURLs(t *testing.T) {
	h, err := NewWithBasePath("/gw")
	require.NoError(t, err)

	body := serveIndex(t, h, "/gw/admin/dashboard").Body.String()

	assert.Contains(t, body, `window.GOMODEL_BASE_PATH="/gw"`)
	assert.Regexp(t, `"/gw/admin/static/assets/index-[^"]+\.js`, body)
	assert.NotContains(t, body, `"/admin/static/`)
}

func TestStatic_ServesSPAAssets(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	sub, err := fs.Sub(content, "static/dist")
	require.NoError(t, err)

	entries, err := fs.ReadDir(sub, "assets")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		rec := serveStatic(t, h, "/admin/static/assets/"+entry.Name())
		assert.Equal(t, http.StatusOK, rec.Code, entry.Name())
		assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable", entry.Name())
	}
}

func TestStatic_ServesFavicon(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	rec := serveStatic(t, h, "/admin/static/favicon.svg")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestStatic_ServesFonts(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	rec := serveStatic(t, h, "/admin/static/fonts/inter.css")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestStatic_NotFound(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	rec := serveStatic(t, h, "/admin/static/nope.js")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestIndex_HasNoExternalResources keeps the dashboard self-contained: no
// CDN scripts, styles, or fonts.
func TestIndex_HasNoExternalResources(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	body := serveIndex(t, h, "/admin/dashboard").Body.String()
	for _, marker := range []string{
		"https://cdn.",
		"http://cdn.",
		"unpkg.com",
		"jsdelivr.net",
		"googleapis.com",
	} {
		assert.NotContains(t, body, marker)
	}
}

// The dashboard holds an admin credential, so it ships browser hardening
// headers, and its CSP must still admit the one inline script it injects.
func TestIndex_SetsSecurityHeaders(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		demoMode bool
	}{
		{name: "root", basePath: "/"},
		{name: "base path and demo mode", basePath: "/gw", demoMode: true},
	}
	inlineScript := regexp.MustCompile(`<script>([^<]*)</script>`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := NewWithDemoMode(tt.basePath, tt.demoMode)
			require.NoError(t, err)

			rec := serveIndex(t, h, "/admin/dashboard")

			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))

			csp := rec.Header().Get("Content-Security-Policy")
			directives := strings.Split(csp, "; ")
			assert.Contains(t, directives, "frame-ancestors 'none'")
			assert.Contains(t, directives, "object-src 'none'")

			// Scripts run only from the bundle and the hashed runtime globals.
			scripts := inlineScript.FindAllStringSubmatch(rec.Body.String(), -1)
			require.Len(t, scripts, 1, "the runtime globals should be the only inline script")
			sum := sha256.Sum256([]byte(scripts[0][1]))
			assert.Contains(t, directives, "script-src 'self' 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		})
	}
}

func TestStatic_SetsNoSniff(t *testing.T) {
	h, err := NewWithBasePath("/")
	require.NoError(t, err)

	rec := serveStatic(t, h, "/admin/static/favicon.svg")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}
