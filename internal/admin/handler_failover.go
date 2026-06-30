package admin

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/failover"
	fallbackresolver "gomodel/internal/fallback"
	"gomodel/internal/providers"
)

type upsertFailoverRuleRequest struct {
	PrimaryModel   string   `json:"primary_model"`
	FallbackModels []string `json:"fallback_models"`
	Enabled        *bool    `json:"enabled,omitempty"`
}

type deleteFailoverRuleRequest struct {
	PrimaryModel string `json:"primary_model"`
}

type generateFailoverRulesRequest struct {
	PrimaryModel string `json:"primary_model"`
	Model        string `json:"model"`
}

// ListFailoverRules handles GET /admin/failover.
//
// @Summary      List failover mappings
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   failover.View
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/failover [get]
func (h *Handler) ListFailoverRules(c *echo.Context) error {
	if err := h.failoverFeatureUnavailable(); err != nil {
		return handleError(c, err)
	}
	views := h.failoverRules.ListViews()
	if views == nil {
		views = []failover.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertFailoverRule handles PUT /admin/failover.
//
// @Summary      Create or update one failover mapping
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        mapping  body      upsertFailoverRuleRequest  true  "Failover mapping"
// @Success      200   {object}  failover.View
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      502   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/failover [put]
func (h *Handler) UpsertFailoverRule(c *echo.Context) error {
	if err := h.failoverFeatureUnavailable(); err != nil {
		return handleError(c, err)
	}
	var req upsertFailoverRuleRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	source := strings.TrimSpace(req.PrimaryModel)
	if source == "" {
		return handleError(c, core.NewInvalidRequestError("primary_model is required", nil))
	}
	enabled := true
	if existing, ok := h.failoverRules.Get(source); ok && existing != nil {
		enabled = existing.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := failover.Rule{
		Source:  source,
		Targets: req.FallbackModels,
		Enabled: enabled,
	}
	if err := h.failoverRules.Upsert(c.Request().Context(), rule); err != nil {
		return handleError(c, failoverWriteError(err))
	}
	if view, ok := h.findFailoverView(source); ok {
		return c.JSON(http.StatusOK, view)
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteFailoverRule handles DELETE /admin/failover.
//
// @Summary      Delete one failover mapping
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  deleteFailoverRuleRequest  true  "Failover primary model to remove"
// @Success      204      "No Content"
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      404      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Failure      503      {object}  core.GatewayError
// @Router       /admin/failover [delete]
func (h *Handler) DeleteFailoverRule(c *echo.Context) error {
	if err := h.failoverFeatureUnavailable(); err != nil {
		return handleError(c, err)
	}
	source, err := failoverDeleteSource(c)
	if err != nil {
		return handleError(c, err)
	}
	err = h.failoverRules.Delete(c.Request().Context(), source)
	switch {
	case err == nil:
		return c.NoContent(http.StatusNoContent)
	case errors.Is(err, failover.ErrNotFound):
		return handleError(c, core.NewNotFoundError("failover mapping not found: "+source))
	default:
		return handleError(c, failoverWriteError(err))
	}
}

func failoverDeleteSource(c *echo.Context) (string, error) {
	var req deleteFailoverRuleRequest
	if err := c.Bind(&req); err != nil {
		return "", core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	source := strings.TrimSpace(req.PrimaryModel)
	if source == "" {
		return "", core.NewInvalidRequestError("primary_model is required", nil)
	}
	return source, nil
}

// ResetFailoverRules handles POST /admin/failover/reset.
//
// @Summary      Reset dashboard-managed failover mappings
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   failover.View
// @Failure      401  {object}  core.GatewayError
// @Failure      502  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/failover/reset [post]
func (h *Handler) ResetFailoverRules(c *echo.Context) error {
	if err := h.failoverFeatureUnavailable(); err != nil {
		return handleError(c, err)
	}
	if err := h.failoverRules.ResetDashboardRules(c.Request().Context()); err != nil {
		return handleError(c, failoverWriteError(err))
	}
	return c.JSON(http.StatusOK, h.failoverRules.ListViews())
}

// GenerateFailoverRules handles POST /admin/failover/generate.
//
// @Summary      Generate failover mapping suggestions
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      generateFailoverRulesRequest  false  "Optional source model filter"
// @Success      200  {array}   failover.View
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/failover/generate [post]
func (h *Handler) GenerateFailoverRules(c *echo.Context) error {
	if err := h.failoverFeatureUnavailable(); err != nil {
		return handleError(c, err)
	}
	if h.registry == nil {
		return handleError(c, featureUnavailableError("failover feature is unavailable"))
	}
	primaryModel, err := failoverGenerateSource(c)
	if err != nil {
		return handleError(c, err)
	}
	resolver := fallbackresolver.NewResolverWithRuleProvider(config.FallbackConfig{Enabled: true}, h.registry, h.failoverRules)
	if resolver == nil {
		return c.JSON(http.StatusOK, []failover.View{})
	}
	suggestions := make([]failover.View, 0)
	for _, model := range h.registry.ListModelsWithProvider() {
		if !modelSupportsCategory(model.Model.Metadata, core.CategoryTextGeneration) {
			continue
		}
		source := strings.TrimSpace(model.Selector)
		if source == "" {
			continue
		}
		if primaryModel != "" && !failoverSourceMatchesModel(primaryModel, source, model) {
			continue
		}
		resolution := &core.RequestModelResolution{
			Requested: core.NewRequestedModelSelector(model.Model.ID, model.ProviderName),
			ResolvedSelector: core.ModelSelector{
				Provider: model.ProviderName,
				Model:    model.Model.ID,
			},
			ProviderName: model.ProviderName,
			ProviderType: model.ProviderType,
		}
		candidates := resolver.SuggestFallbacks(resolution, core.OperationChatCompletions)
		if len(candidates) == 0 {
			continue
		}
		targets := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			targets = append(targets, candidate.QualifiedModel())
		}
		suggestions = append(suggestions, failover.View{
			Source:        source,
			Targets:       targets,
			Enabled:       true,
			ManagedSource: failover.ManagedSourceDashboard,
		})
	}
	return c.JSON(http.StatusOK, suggestions)
}

func failoverGenerateSource(c *echo.Context) (string, error) {
	source := strings.TrimSpace(c.QueryParam("primary_model"))
	if source == "" {
		source = strings.TrimSpace(c.QueryParam("model"))
	}
	if source != "" || c.Request().ContentLength == 0 {
		return source, nil
	}
	var req generateFailoverRulesRequest
	if err := c.Bind(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return source, nil
		}
		return "", core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	source = strings.TrimSpace(req.PrimaryModel)
	if source == "" {
		source = strings.TrimSpace(req.Model)
	}
	return source, nil
}

func failoverSourceMatchesModel(filter string, source string, model providers.ModelWithProvider) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	if filter == source || filter == strings.TrimSpace(model.Model.ID) {
		return true
	}
	if model.ProviderName != "" && filter == model.ProviderName+"/"+model.Model.ID {
		return true
	}
	return false
}

func modelSupportsCategory(meta *core.ModelMetadata, category core.ModelCategory) bool {
	if meta == nil || len(meta.Categories) == 0 {
		return true
	}
	for _, candidate := range meta.Categories {
		if candidate == category {
			return true
		}
	}
	return false
}

func (h *Handler) findFailoverView(source string) (failover.View, bool) {
	for _, view := range h.failoverRules.ListViews() {
		if view.Source == source {
			return view, true
		}
	}
	return failover.View{}, false
}

func (h *Handler) failoverFeatureUnavailable() error {
	if h.failoverRules == nil || dashboardFlagDisabled(h.runtimeConfig.FailoverEnabled) {
		return featureUnavailableError("failover feature is unavailable")
	}
	return nil
}

func dashboardFlagDisabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false", "0":
		return true
	default:
		return false
	}
}

func failoverWriteError(err error) error {
	if errors.Is(err, failover.ErrManaged) {
		return core.NewInvalidRequestError("failover mapping is managed by configuration and cannot be changed in the dashboard", err)
	}
	return core.NewProviderError("admin", http.StatusBadGateway, err.Error(), err)
}
