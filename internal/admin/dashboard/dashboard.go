// Package dashboard provides the embedded admin dashboard UI for GOModel.
package dashboard

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"

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
	tmpl, err := template.ParseFS(content, "templates/*.html")
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
