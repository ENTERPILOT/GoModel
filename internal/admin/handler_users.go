package admin

import (
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
	InheritedFrom []string   `json:"inherited_from"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
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
	return c.JSON(http.StatusOK, userListResponse{Users: h.userNodes()})
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
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	if _, err := h.users.Upsert(c.Request().Context(), users.User{
		UserPath:      req.UserPath,
		AllowedModels: req.AllowedModels,
		Description:   req.Description,
	}); err != nil {
		return handleError(c, userWriteError(err))
	}
	return c.JSON(http.StatusOK, userListResponse{Users: h.userNodes()})
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
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	if err := h.users.Delete(c.Request().Context(), userPath); err != nil {
		if errors.Is(err, users.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("user not found: "+userPath))
		}
		return handleError(c, userWriteError(err))
	}
	return c.JSON(http.StatusOK, userListResponse{Users: h.userNodes()})
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
// and all their ancestors, sorted by path.
func (h *Handler) userNodes() []userNodeResponse {
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

	result := make([]userNodeResponse, 0, len(nodes))
	for userPath, node := range nodes {
		for _, constraint := range h.users.Constraints(userPath) {
			if constraint.UserPath != userPath {
				node.InheritedFrom = append(node.InheritedFrom, constraint.UserPath)
			}
		}
		result = append(result, *node)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserPath < result[j].UserPath })
	return result
}
