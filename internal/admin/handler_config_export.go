package admin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/version"
)

// ConfigExportSource is what a configuration export starts from: the
// configuration this gateway loaded, resolved, the providers it resolved from
// config.yaml and environment variables (see providers.InitResult.ExportProviders),
// and the Secrets that resolved them.
type ConfigExportSource struct {
	Config    *config.Config
	Providers map[string]config.RawProviderConfig
	Secrets   *config.Secrets
}

// WithConfigExport enables GET /admin/config/export.
func WithConfigExport(source ConfigExportSource) Option {
	return func(h *Handler) {
		if source.Config == nil {
			return
		}
		h.configExport = &source
	}
}

// configExportFilename is the name the export downloads under.
const configExportFilename = "gomodel-config.yaml"

// ExportConfig handles GET /admin/config/export.
//
// @Summary      Export the effective configuration as config.yaml
// @Description  Returns every setting in effect, from defaults, config.yaml, environment variables, and objects managed in the dashboard, as a config.yaml document. No secret is included: a secret is written as the reference it was configured with (${env:...}, ${file:...}, ${vault:...}) or as ${env:NAME}, with a comment naming the variable to set.
// @Tags         admin
// @Produce      application/yaml
// @Security     BearerAuth
// @Success      200  {string}  string  "config.yaml document"
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/config/export [get]
func (h *Handler) ExportConfig(c *echo.Context) error {
	if h.configExport == nil {
		return handleError(c, featureUnavailableError("configuration export is unavailable"))
	}
	doc, err := h.configExportDocument(c.Request().Context(), time.Now().UTC())
	if err != nil {
		return handleError(c, err)
	}
	data, err := h.configExport.Secrets.ExportYAML(doc, os.Environ())
	if err != nil {
		return handleError(c, err)
	}
	header := c.Response().Header()
	header.Set(echo.HeaderContentDisposition, `attachment; filename="`+configExportFilename+`"`)
	header.Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "application/yaml; charset=utf-8", data)
}

// configExportDocument merges the objects managed in the dashboard into the
// loaded configuration. Objects declared in config.yaml or env keep their
// declaration, which shadows a dashboard row of the same name.
func (h *Handler) configExportDocument(ctx context.Context, now time.Time) (config.ExportDocument, error) {
	source := h.configExport
	cfg := *source.Config
	notExported := []string{"managed API keys", "workflows", "model pricing overrides", "runtime settings"}

	providers, skipped, err := h.exportProviders(ctx, source.Providers)
	if err != nil {
		return config.ExportDocument{}, err
	}
	if len(skipped) > 0 {
		notExported = append(notExported, "disabled providers ("+strings.Join(skipped, ", ")+")")
	}
	cfg.VirtualModels = h.exportVirtualModels(cfg.VirtualModels)
	cfg.Users = h.exportUsers(cfg.Users)
	cfg.MCP = h.exportMCP(cfg.MCP)
	cfg.Tagging.Headers = h.exportTaggingHeaders(cfg.Tagging.Headers)
	cfg.Budgets = h.exportBudgets(cfg.Budgets)
	cfg.RateLimits = h.exportRateLimits(cfg.RateLimits)
	secretFields, dashboardGuardrails := h.exportGuardrails(cfg.Guardrails.Rules)
	if len(dashboardGuardrails) > 0 {
		notExported = append(notExported, "guardrails created in the dashboard ("+strings.Join(dashboardGuardrails, ", ")+")")
	}

	return config.ExportDocument{
		Config:       &cfg,
		Providers:    providers,
		SecretFields: secretFields,
		Header:       configExportHeader(now, notExported),
	}, nil
}

func configExportHeader(now time.Time, notExported []string) string {
	return strings.Join([]string{
		fmt.Sprintf("GoModel configuration exported %s from %s %s.", now.Format(time.RFC3339), version.App, version.Version),
		"Every setting in effect: defaults, config.yaml, environment variables,",
		"and objects managed in the dashboard. Load it as config/config.yaml;",
		"objects created in the dashboard are then read-only there.",
		"",
		"No secret is included. A secret is written as the reference it was",
		"configured with (${env:...}, ${file:...}, ${vault:...}), or as ${env:NAME}",
		"with a comment naming the variable to set before loading this file.",
		"",
		"Not exported: " + strings.Join(notExported, "; ") + ".",
	}, "\n")
}
