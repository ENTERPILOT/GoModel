package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// deleteManagedResource is the common shape behind every admin DELETE
// endpoint for a config/env-shadowable store (MCP servers, provider
// credentials, ...): validate the path name, reject config/env-declared
// (managed) resources as read-only, delete, and translate the store's
// not-found sentinel into a 404. resourceNoun names the resource in error
// messages (e.g. "mcp server", "provider").
func deleteManagedResource(
	c *echo.Context,
	resourceNoun string,
	isManaged func(name string) bool,
	del func(ctx context.Context, name string) error,
	notFound error,
	writeErr func(error) error,
) error {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	if isManaged(name) {
		return handleError(c, core.NewInvalidRequestError(resourceNoun+" "+name+" is managed by config/env and is read-only", nil))
	}

	if err := del(c.Request().Context(), name); err != nil {
		if errors.Is(err, notFound) {
			return handleError(c, core.NewNotFoundError(resourceNoun+" not found: "+name))
		}
		return handleError(c, writeErr(err))
	}
	return c.NoContent(http.StatusNoContent)
}
