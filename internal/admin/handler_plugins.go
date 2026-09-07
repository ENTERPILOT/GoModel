package admin

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// pluginView is one catalog entry as GET /admin/plugins renders it.
type pluginView struct {
	Name string `json:"name"`
	// Label is the name as a title ("header_edit" reads "Header Edit").
	Label       string                 `json:"label"`
	Version     string                 `json:"version,omitempty"`
	Description string                 `json:"description,omitempty"`
	Kinds       []string               `json:"kinds"`
	Mutates     bool                   `json:"mutates"`
	Guardrail   bool                   `json:"guardrail"`
	Source      string                 `json:"source"`
	Fields      []guardrails.TypeField `json:"fields"`
	RouteFields []guardrails.TypeField `json:"route_fields"`
	Health      string                 `json:"health"`
	Error       string                 `json:"error"`
	BuiltWith   *pluginapi.BuildInfo   `json:"built_with,omitempty"`
}

// ListPlugins handles GET /admin/plugins: every loaded plugin type with its
// manifest, hook kinds, config schema, source and health.
func (h *Handler) ListPlugins(c *echo.Context) error {
	if h.pluginCatalog == nil {
		return handleError(c, featureUnavailableError("plugin system is disabled; set PLUGINS_ENABLED=true"))
	}
	entries := h.pluginCatalog.Entries()
	views := make([]pluginView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, pluginViewFromEntry(entry))
	}
	return c.JSON(http.StatusOK, views)
}

func pluginViewFromEntry(entry plugins.Entry) pluginView {
	view := pluginView{
		Name:        entry.Name,
		Label:       guardrails.TypeLabel(entry.Name),
		Version:     entry.Manifest.Version,
		Description: entry.Manifest.Description,
		Kinds:       []string{},
		Mutates:     entry.Manifest.Mutates,
		Guardrail:   entry.Manifest.Guardrail,
		Source:      string(entry.Source),
		Fields:      guardrails.TypeFieldsFromSchema(entry.Manifest.ConfigSchema, pluginapi.ScopeInstance),
		RouteFields: guardrails.TypeFieldsFromSchema(entry.Manifest.ConfigSchema, pluginapi.ScopeRoute),
		Health:      entry.Health,
	}
	for _, kind := range entry.Kinds {
		view.Kinds = append(view.Kinds, string(kind))
	}
	if entry.Err != nil {
		view.Error = entry.Err.Error()
	}
	if entry.Manifest.BuiltWith != (pluginapi.BuildInfo{}) {
		built := entry.Manifest.BuiltWith
		view.BuiltWith = &built
	}
	return view
}
