package admin

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/users"
)

// userNodeResponse is one node of the user-path tree: a stored policy, a
// path some auth key is bound to, or an implied ancestor of either.
type userNodeResponse struct {
	UserPath string `json:"user_path"`
	// AllowedModels is the node's own allowlist; empty means it does not
	// restrict models itself.
	AllowedModels []string `json:"allowed_models"`
	Description   string   `json:"description,omitempty"`
	// Configured reports whether a policy row exists for this exact path.
	Configured bool `json:"configured"`
	// Managed reports whether the policy is declared in config and read-only.
	Managed bool `json:"managed,omitempty"`
	// KeyCount is the number of managed auth keys bound exactly to this path.
	KeyCount int `json:"key_count"`
	// InheritedFrom lists ancestor paths whose allowlists also apply here,
	// root first.
	InheritedFrom []string `json:"inherited_from"`
	// Restricted reports whether any allowlist (own or inherited) applies.
	Restricted bool `json:"restricted"`
	// EffectiveModels lists the catalog models a request on this path may use,
	// evaluated through the same authorizer inference uses. Nil when no model
	// catalog is available.
	EffectiveModels []string   `json:"effective_models"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

type userListResponse struct {
	Users []userNodeResponse `json:"users"`
}

type upsertUserRequest struct {
	UserPath      string   `json:"user_path"`
	AllowedModels []string `json:"allowed_models"`
	Description   string   `json:"description,omitempty"`
}

// ListUsers handles GET /admin/users. It returns the user-path tree derived
// from stored policies and managed auth keys, each node with its own
// allowlist and the ancestors that restrict it.
func (h *Handler) ListUsers(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	return c.JSON(http.StatusOK, userListResponse{Users: h.userNodes(requestScope(c))})
}

// UpsertUser handles PUT /admin/users.
func (h *Handler) UpsertUser(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	var req upsertUserRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if err := requireUserPathInScope(c, req.UserPath); err != nil {
		return handleError(c, err)
	}
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	if _, err := h.users.Upsert(c.Request().Context(), users.User{
		UserPath:      req.UserPath,
		AllowedModels: req.AllowedModels,
		Description:   req.Description,
	}); err != nil {
		return handleError(c, userWriteError(err))
	}
	return c.JSON(http.StatusOK, userListResponse{Users: h.userNodes(requestScope(c))})
}

// DeleteUser handles DELETE /admin/users?user_path=...
func (h *Handler) DeleteUser(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	userPath := strings.TrimSpace(c.QueryParam("user_path"))
	if userPath == "" {
		return handleError(c, core.NewInvalidRequestError("user_path is required", nil))
	}
	if err := requireUserPathInScope(c, userPath); err != nil {
		return handleError(c, err)
	}
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	if err := h.users.Delete(c.Request().Context(), userPath); err != nil {
		if errors.Is(err, users.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("user not found: "+userPath))
		}
		return handleError(c, userWriteError(err))
	}
	return c.JSON(http.StatusOK, userListResponse{Users: h.userNodes(requestScope(c))})
}

// requireUserPathInScope rejects writes to user nodes outside the caller's
// scope. Unparseable paths pass through so the service reports the usual
// validation error.
func requireUserPathInScope(c *echo.Context, raw string) error {
	scope := requestScope(c)
	if scope.Global() {
		return nil
	}
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return nil
	}
	if !scope.Allows(userPath) {
		return userPathOutOfScopeError("user_path")
	}
	return nil
}

// userCatalog lists every registered model as a selector, or nil without a registry.
func (h *Handler) userCatalog() []core.ModelSelector {
	if h.registry == nil {
		return nil
	}
	models := h.registry.ListModelsWithProvider()
	catalog := make([]core.ModelSelector, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, core.ModelSelector{
			Provider: strings.TrimSpace(model.ProviderName),
			Model:    strings.TrimSpace(model.Model.ID),
		})
	}
	return catalog
}

// effectiveModels evaluates the catalog for a request carrying ctx (user path
// and, for a key, its allowlist) through the virtual-models authorizer when
// wired (model-side rows plus user policies), falling back to the user
// policies alone. Nil without a catalog or any authorizer.
func (h *Handler) effectiveModels(ctx context.Context, catalog []core.ModelSelector) []string {
	if catalog == nil {
		return nil
	}
	var allows func(context.Context, core.ModelSelector) bool
	switch {
	case h.virtualModels != nil:
		allows = h.virtualModels.AllowsModel
	case h.users != nil:
		allows = h.users.AllowsModel
	default:
		return nil
	}
	result := make([]string, 0, len(catalog))
	for _, selector := range catalog {
		if allows(ctx, selector) {
			result = append(result, selector.QualifiedModel())
		}
	}
	return result
}

func userWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, users.ErrManaged) {
		return core.NewInvalidRequestErrorWithStatus(http.StatusConflict, err.Error(), err).WithCode("user_managed")
	}
	if users.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("users", http.StatusBadGateway, err.Error(), err)
}

// userNodes builds the tree: every stored policy, every auth-key user path,
// and all their ancestors, sorted by path. Only nodes inside scope are
// returned, so a scoped admin sees its subtree without the ancestors above it.
func (h *Handler) userNodes(scope core.AccessScope) []userNodeResponse {
	nodes := make(map[string]*userNodeResponse)
	ensure := func(userPath string) *userNodeResponse {
		for _, ancestor := range core.UserPathAncestors(userPath) {
			if _, ok := nodes[ancestor]; !ok {
				nodes[ancestor] = &userNodeResponse{UserPath: ancestor, AllowedModels: []string{}, InheritedFrom: []string{}}
			}
		}
		return nodes[userPath]
	}

	for _, user := range h.users.List() {
		node := ensure(user.UserPath)
		node.Configured = true
		node.Managed = user.Managed
		node.Description = user.Description
		if len(user.AllowedModels) > 0 {
			node.AllowedModels = user.AllowedModels
		}
		if !user.CreatedAt.IsZero() {
			createdAt := user.CreatedAt
			node.CreatedAt = &createdAt
		}
		if !user.UpdatedAt.IsZero() {
			updatedAt := user.UpdatedAt
			node.UpdatedAt = &updatedAt
		}
	}
	if h.authKeys != nil {
		for _, key := range h.authKeys.ListViews() {
			userPath, err := core.NormalizeUserPath(key.UserPath)
			if err != nil || userPath == "" {
				continue
			}
			ensure(userPath).KeyCount++
		}
	}

	catalog := h.userCatalog()
	result := make([]userNodeResponse, 0, len(nodes))
	for userPath, node := range nodes {
		if !scope.Allows(userPath) {
			continue
		}
		constraints := h.users.Constraints(userPath)
		node.Restricted = len(constraints) > 0
		for _, constraint := range constraints {
			if constraint.UserPath != userPath {
				node.InheritedFrom = append(node.InheritedFrom, constraint.UserPath)
			}
		}
		node.EffectiveModels = h.effectiveModels(core.WithEffectiveUserPath(context.Background(), userPath), catalog)
		result = append(result, *node)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserPath < result[j].UserPath })
	return result
}
