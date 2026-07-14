// Package dashboard provides the embedded admin dashboard UI for GoModel.
package dashboard

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/version"

	"github.com/labstack/echo/v5"
)

//go:embed templates/*.html static/css/*.css static/js/*.js static/js/modules/*.js static/vendor/*.js static/fonts/*.css static/fonts/*.woff2 static/*.svg
var content embed.FS

// Handler serves the admin dashboard UI.
type Handler struct {
	indexTmpl *template.Template
	staticFS  http.Handler
	basePath  string
}

// NewWithBasePath creates a dashboard handler for an app mounted under basePath.
// It parses templates and sets up the static file server.
func NewWithBasePath(basePath string) (*Handler, error) {
	basePath = config.NormalizeBasePath(basePath)
	assetVersions, err := buildFrontendAssetVersions()
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("layout").Funcs(template.FuncMap{
		"assetURL": func(path string) string {
			return assetURL(basePath, path, assetVersions)
		},
		"appURL": func(path string) string {
			return config.JoinBasePath(basePath, path)
		},
	}).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, err
	}

	staticSub, err := fs.Sub(content, "static")
	if err != nil {
		return nil, err
	}

	return &Handler{
		indexTmpl: tmpl,
		staticFS:  http.StripPrefix("/admin/static/", http.FileServer(http.FS(staticSub))),
		basePath:  basePath,
	}, nil
}

type templateData struct {
	BasePath string
	Version  string
}

// Index serves GET /admin/dashboard — the main dashboard page.
func (h *Handler) Index(c *echo.Context) error {
	var buf bytes.Buffer
	if err := h.indexTmpl.ExecuteTemplate(&buf, "layout", templateData{BasePath: h.basePath, Version: version.Info()}); err != nil {
		slog.Error("failed to render admin dashboard", "path", c.Request().URL.Path, "error", err)
		return err
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(http.StatusOK)
	_, err := buf.WriteTo(c.Response())
	if err != nil {
		slog.Error("failed to write admin dashboard response", "path", c.Request().URL.Path, "error", err)
	}
	return err
}

// Static serves GET /admin/static/* — embedded CSS/JS assets.
func (h *Handler) Static(c *echo.Context) error {
	h.staticFS.ServeHTTP(c.Response(), c.Request())
	return nil
}

func buildAssetVersions(paths ...string) (map[string]string, error) {
	versions := make(map[string]string, len(paths))
	for _, path := range paths {
		normalizedPath := strings.TrimLeft(strings.TrimSpace(path), "/")
		if normalizedPath == "" {
			continue
		}
		data, err := content.ReadFile("static/" + normalizedPath)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		versions[normalizedPath] = hex.EncodeToString(sum[:6])
	}
	return versions, nil
}

func buildFrontendAssetVersions() (map[string]string, error) {
	paths := []string{}
	for _, pattern := range []string{"static/css/*.css", "static/js/*.js", "static/js/modules/*.js"} {
		matches, err := fs.Glob(content, pattern)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			paths = append(paths, strings.TrimPrefix(match, "static/"))
		}
	}
	return buildAssetVersions(paths...)
}

func assetURL(basePath, assetPath string, versions map[string]string) string {
	normalizedPath := strings.TrimLeft(strings.TrimSpace(assetPath), "/")
	if normalizedPath == "" {
		return config.JoinBasePath(basePath, "/admin/static/")
	}
	urlPath := config.JoinBasePath(basePath, "/admin/static/"+normalizedPath)
	if v := versions[normalizedPath]; v != "" {
		return urlPath + "?v=" + v
	}
	return urlPath
}
