package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/headerpolicy"
)

type deleteHeaderPolicyRequest struct {
	Name string `json:"name"`
}

// ListHeaderPolicies handles GET /admin/header-policies.
func (h *Handler) ListHeaderPolicies(c *echo.Context) error {
	if h.headerPolicyDefs == nil {
		return handleError(c, featureUnavailableError("header policies feature is unavailable"))
	}
	views := h.headerPolicyDefs.ListViews()
	if views == nil {
		views = []headerpolicy.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertHeaderPolicy handles PUT /admin/header-policies.
func (h *Handler) UpsertHeaderPolicy(c *echo.Context) error {
	if h.headerPolicyDefs == nil {
		return handleError(c, featureUnavailableError("header policies feature is unavailable"))
	}
	var definition headerpolicy.Definition
	if err := c.Bind(&definition); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	definition.Name = strings.TrimSpace(definition.Name)
	if definition.Name == "" {
		return handleError(c, core.NewInvalidRequestError("header policy name is required", nil))
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	if h.guardrailDefs != nil {
		if _, exists := h.guardrailDefs.Get(definition.Name); exists {
			return handleError(c, core.NewInvalidRequestError("name is already used by a guardrail: "+definition.Name, nil))
		}
	}
	previous, existed := h.headerPolicyDefs.Get(definition.Name)
	if err := h.headerPolicyDefs.Upsert(c.Request().Context(), definition); err != nil {
		return handleError(c, headerPolicyWriteError(err))
	}
	rollback := func(rollbackCtx context.Context) error {
		if existed {
			return h.headerPolicyDefs.Upsert(rollbackCtx, *previous)
		}
		return h.headerPolicyDefs.Delete(rollbackCtx, definition.Name)
	}
	if err := h.refreshWorkflowsOrRollback(c.Request().Context(), rollback); err != nil {
		return handleError(c, err)
	}
	stored, ok := h.headerPolicyDefs.Get(definition.Name)
	if !ok {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, headerpolicy.ViewFromDefinition(*stored))
}

// DeleteHeaderPolicy handles DELETE /admin/header-policies.
//
//nolint:dupl // Keep this resource handler explicit and independent from guardrail lifecycle semantics.
func (h *Handler) DeleteHeaderPolicy(c *echo.Context) error {
	if h.headerPolicyDefs == nil {
		return handleError(c, featureUnavailableError("header policies feature is unavailable"))
	}
	var req deleteHeaderPolicyRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("header policy name is required", nil))
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	references, err := h.activeWorkflowHeaderPolicyReferences(c.Request().Context(), name)
	if err != nil {
		return handleError(c, err)
	}
	if len(references) > 0 {
		return handleError(c, core.NewInvalidRequestError("header policy is used by active workflows: "+strings.Join(references, ", "), nil))
	}
	previous, existed := h.headerPolicyDefs.Get(name)
	if err := h.headerPolicyDefs.Delete(c.Request().Context(), name); err != nil {
		if errors.Is(err, headerpolicy.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("header policy not found: "+name))
		}
		return handleError(c, headerPolicyWriteError(err))
	}
	rollback := func(rollbackCtx context.Context) error {
		if !existed {
			return nil
		}
		return h.headerPolicyDefs.Upsert(rollbackCtx, *previous)
	}
	if err := h.refreshWorkflowsOrRollback(c.Request().Context(), rollback); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
