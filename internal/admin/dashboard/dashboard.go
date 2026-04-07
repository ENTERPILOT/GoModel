// Package dashboard provides the embedded admin dashboard UI for GOModel.
package dashboard

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

//go:embed templates/*.html static/css/*.css static/js/*.js static/js/modules/*.js static/*.svg
var content embed.FS

// Handler serves the admin dashboard UI.
type Handler struct {
	indexTmpl *template.Template
	staticFS  http.Handler
}

// New creates a new dashboard handler with parsed templates and static file server.
func New() (*Handler, error) {
	assetVersions, err := buildAssetVersions("css/dashboard.css")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("layout").Funcs(template.FuncMap{
		"assetURL": func(path string) string {
			return assetURL(path, assetVersions)
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
	}, nil
}

// Index serves GET /admin/dashboard — the main dashboard page.
func (h *Handler) Index(c *echo.Context) error {
	var buf bytes.Buffer
	if err := h.indexTmpl.ExecuteTemplate(&buf, "layout", nil); err != nil {
		return err
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(http.StatusOK)
	_, err := buf.WriteTo(c.Response())
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

func assetURL(path string, versions map[string]string) string {
	normalizedPath := strings.TrimLeft(strings.TrimSpace(path), "/")
	if normalizedPath == "" {
		return "/admin/static/"
	}
	urlPath := "/admin/static/" + normalizedPath
	if version := versions[normalizedPath]; version != "" {
		return urlPath + "?v=" + version
	}
	return urlPath
}
