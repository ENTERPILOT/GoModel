// Package dashboard provides the embedded admin dashboard UI for GoModel.
//
// The UI is a Svelte single-page app built from web/dashboard into
// static/dist by `make frontend` (locally) or the CI `frontend` job. The
// build output is not committed; see docs/adr/0010-dashboard-built-in-ci.md.
// This handler serves the built index.html — with runtime globals (base path,
// version, demo mode) injected — and the hashed static assets under
// /admin/static/.
package dashboard

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/version"

	"github.com/labstack/echo/v5"
)

// static/ holds a committed placeholder so the embed compiles on a clean
// checkout; static/dist is produced by the dashboard build and never committed.
//
//go:embed all:static
var content embed.FS

// Handler serves the admin dashboard UI.
type Handler struct {
	indexHTML []byte
	staticFS  http.Handler
	basePath  string
}

// NewWithBasePath creates a dashboard handler for an app mounted under basePath.
func NewWithBasePath(basePath string) (*Handler, error) {
	return NewWithDemoMode(basePath, false)
}

// NewWithDemoMode creates a dashboard handler and controls whether the demo
// warning is rendered by the SPA.
func NewWithDemoMode(basePath string, demoMode bool) (*Handler, error) {
	basePath = config.NormalizeBasePath(basePath)

	indexHTML, err := buildIndexHTML(basePath, demoMode)
	if err != nil {
		return nil, err
	}

	staticSub, err := fs.Sub(content, "static/dist")
	if err != nil {
		return nil, err
	}

	return &Handler{
		indexHTML: indexHTML,
		staticFS: http.StripPrefix(
			"/admin/static/",
			http.FileServer(http.FS(staticSub)),
		),
		basePath: basePath,
	}, nil
}

// buildIndexHTML loads the built SPA entry point, injects the runtime
// globals the app reads on boot, and rewrites asset URLs when the app is
// mounted under a base path.
func buildIndexHTML(basePath string, demoMode bool) ([]byte, error) {
	raw, err := content.ReadFile("static/dist/index.html")
	if err != nil {
		return nil, fmt.Errorf(
			"dashboard assets missing (run `make frontend` to build web/dashboard): %w",
			err,
		)
	}

	html := string(raw)
	if basePath != "/" {
		prefixed := config.JoinBasePath(basePath, "/admin/static/")
		html = strings.ReplaceAll(html, `"/admin/static/`, `"`+prefixed)
	}

	globals := fmt.Sprintf(
		`<script>window.GOMODEL_BASE_PATH=%q;window.GOMODEL_VERSION=%q;window.GOMODEL_DEMO_MODE=%t;</script>`,
		basePath, version.Info(), demoMode,
	)
	if !strings.Contains(html, "<head>") {
		return nil, fmt.Errorf("dashboard index.html has no <head> element")
	}
	html = strings.Replace(html, "<head>", "<head>\n    "+globals, 1)

	return []byte(html), nil
}

// Index serves GET /admin/dashboard and every /admin/dashboard/* route — the
// SPA handles routing client-side.
func (h *Handler) Index(c *echo.Context) error {
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	// The entry point references hashed assets; it must always be fresh.
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().WriteHeader(http.StatusOK)
	_, err := bytes.NewReader(h.indexHTML).WriteTo(c.Response())
	if err != nil {
		slog.Error("failed to write admin dashboard response", "path", c.Request().URL.Path, "error", err)
	}
	return err
}

// Static serves GET /admin/static/* — embedded SPA assets.
func (h *Handler) Static(c *echo.Context) error {
	// Vite emits content-hashed filenames under assets/, safe to cache hard.
	if strings.Contains(c.Request().URL.Path, "/assets/") {
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	h.staticFS.ServeHTTP(c.Response(), c.Request())
	return nil
}
