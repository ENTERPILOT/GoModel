package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/workflows"
)

type createWorkflowRequest struct {
	ScopeProviderName   string            `json:"scope_provider_name,omitempty"`
	LegacyScopeProvider string            `json:"scope_provider,omitempty"`
	ScopeModel          string            `json:"scope_model,omitempty"`
	ScopeUserPath       string            `json:"scope_user_path,omitempty"`
	Name                string            `json:"name"`
	Description         string            `json:"description,omitempty"`
	Payload             workflows.Payload `json:"workflow_payload"`
}

func (h *Handler) ListWorkflows(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, featureUnavailableError("workflows feature is unavailable"))
	}

	views, err := h.workflows.ListViews(c.Request().Context())
	if err != nil {
		return handleError(c, err)
	}
	if views == nil {
		views = []workflows.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// GetWorkflow handles GET /admin/workflows/:id
func (h *Handler) GetWorkflow(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, featureUnavailableError("workflows feature is unavailable"))
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("workflow id is required", nil))
	}

	view, err := h.workflows.GetView(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("workflow not found: "+id))
		}
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, view)
}

// ListWorkflowGuardrails handles GET /admin/workflows/guardrails
func (h *Handler) ListWorkflowGuardrails(c *echo.Context) error {
	if h.guardrails == nil {
		return c.JSON(http.StatusOK, []string{})
	}

	return c.JSON(http.StatusOK, h.guardrails.Names())
}

// CreateWorkflow handles POST /admin/workflows
func (h *Handler) CreateWorkflow(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, featureUnavailableError("workflows feature is unavailable"))
	}

	var req createWorkflowRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	scopeProviderName := strings.TrimSpace(req.ScopeProviderName)
	if scopeProviderName == "" {
		scopeProviderName = strings.TrimSpace(req.LegacyScopeProvider)
	}
	scopeModel := strings.TrimSpace(req.ScopeModel)

	scopeUserPath, err := normalizeUserPathQueryParam("scope_user_path", req.ScopeUserPath)
	if err != nil {
		return handleError(c, err)
	}

	scopeProviderName, err = h.validateWorkflowScope(scopeProviderName, scopeModel)
	if err != nil {
		return handleError(c, err)
	}

	if err := h.validateWorkflowGuardrails(req.Payload); err != nil {
		return handleError(c, err)
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	version, err := h.workflows.Create(c.Request().Context(), workflows.CreateInput{
		Scope: workflows.Scope{
			Provider: scopeProviderName,
			Model:    scopeModel,
			UserPath: scopeUserPath,
		},
		Activate:    true,
		Name:        req.Name,
		Description: req.Description,
		Payload:     req.Payload,
	})
	if err != nil {
		return handleError(c, workflowWriteError(err))
	}
	if version == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusCreated, version)
}

// DeactivateWorkflow handles POST /admin/workflows/:id/deactivate
func (h *Handler) DeactivateWorkflow(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, featureUnavailableError("workflows feature is unavailable"))
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("workflow id is required", nil))
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	if err := h.workflows.Deactivate(c.Request().Context(), id); err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("workflow not found: "+id))
		}
		return handleError(c, workflowWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) refreshWorkflowsAfterGuardrailChange(ctx context.Context) error {
	if h.workflows == nil {
		return nil
	}
	if err := h.workflows.Refresh(ctx); err != nil {
		return err
	}
	return nil
}

func (h *Handler) activeWorkflowGuardrailReferences(ctx context.Context, name string) ([]string, error) {
	if h.workflows == nil {
		return nil, nil
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}

	views, err := h.workflows.ListViews(ctx)
	if err != nil {
		return nil, err
	}

	references := make([]string, 0)
	for _, view := range views {
		if !view.Payload.Features.Guardrails {
			continue
		}
		for _, step := range view.Payload.Guardrails {
			if strings.TrimSpace(step.Ref) != name {
				continue
			}
			references = append(references, view.ScopeDisplay)
			break
		}
	}
	sort.Strings(references)
	return references, nil
}

func (h *Handler) validateWorkflowGuardrails(payload workflows.Payload) error {
	if !payload.Features.Guardrails || len(payload.Guardrails) == 0 {
		return nil
	}
	if h.guardrails == nil {
		return featureUnavailableError("guardrail registry is unavailable for workflow authoring")
	}

	known := make(map[string]struct{}, h.guardrails.Len())
	for _, name := range h.guardrails.Names() {
		known[name] = struct{}{}
	}
	for _, step := range payload.Guardrails {
		ref := strings.TrimSpace(step.Ref)
		if ref == "" {
			continue
		}
		if _, ok := known[ref]; !ok {
			return core.NewInvalidRequestError("unknown guardrail ref: "+ref, nil)
		}
	}
	return nil
}

func (h *Handler) validateWorkflowScope(scopeProviderName, scopeModel string) (string, error) {
	scopeProviderName = strings.TrimSpace(scopeProviderName)
	scopeModel = strings.TrimSpace(scopeModel)

	if scopeProviderName == "" {
		if scopeModel != "" {
			return "", core.NewInvalidRequestError("scope_model requires scope_provider_name", nil)
		}
		return "", nil
	}
	if h.registry == nil {
		return "", core.NewInvalidRequestError("provider registry is unavailable for workflow provider-name validation", nil)
	}
	if !slices.Contains(h.registry.ProviderNames(), scopeProviderName) {
		if resolvedProviderName := strings.TrimSpace(h.registry.GetProviderNameForType(scopeProviderName)); resolvedProviderName != "" {
			scopeProviderName = resolvedProviderName
		}
	}
	if !slices.Contains(h.registry.ProviderNames(), scopeProviderName) {
		return "", core.NewInvalidRequestError("unknown provider name: "+scopeProviderName, nil)
	}
	if scopeModel == "" {
		return scopeProviderName, nil
	}

	for _, model := range h.registry.ListModelsWithProvider() {
		if model.ProviderName == scopeProviderName && model.Model.ID == scopeModel {
			return scopeProviderName, nil
		}
	}
	return "", core.NewInvalidRequestError("unknown model for provider name "+scopeProviderName+": "+scopeModel, nil)
}
