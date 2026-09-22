package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mediastore"
)

// WithMediaStore enables GET /admin/media/{id}, which serves the audio and
// images the audit log references by media id.
func WithMediaStore(store *mediastore.Service) Option {
	return func(h *Handler) {
		h.media = store
	}
}

// Media handles GET /admin/media/:id
//
// @Summary      Download a stored media object
// @Description  Streams the bytes of an audio or image object the audit log
// @Description  references by media_id, with its content type. Range requests
// @Description  are honored so browser players can seek. An object outside the
// @Description  caller's user-path scope is reported as missing.
// @Tags         admin
// @Security     BearerAuth
// @Produce      octet-stream
// @Param        id   path      string  true  "Media object id"
// @Success      200  {file}    file
// @Success      206  {file}    file
// @Failure      400  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/media/{id} [get]
func (h *Handler) Media(c *echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("media id is required", nil))
	}
	if h.media == nil {
		return handleError(c, featureUnavailableError("media storage is unavailable"))
	}
	// Ids have one shape; anything else is reported as missing before it can
	// reach a store query.
	if !mediastore.ValidID(id) {
		return handleError(c, mediaNotFound(id))
	}
	ctx := c.Request().Context()
	object, err := h.media.Get(ctx, id)
	if err != nil {
		return handleError(c, mediaError(id, err))
	}
	// An object outside the caller's scope is reported exactly like a
	// missing one so ids from other tenants cannot be probed.
	if !requestScope(c).Allows(object.UserPath) {
		return handleError(c, mediaNotFound(id))
	}
	_, reader, err := h.media.Open(ctx, id)
	if err != nil {
		return handleError(c, mediaError(id, err))
	}
	defer func() { _ = reader.Close() }()

	header := c.Response().Header()
	header.Set("Content-Type", object.ContentType)
	// Only passive audio and raster image types render inline; anything
	// else downloads, so a stored object can never run as a page.
	if mediastore.Inline(object.ContentType) {
		header.Set("Content-Disposition", "inline")
	} else {
		header.Set("Content-Disposition", `attachment; filename="`+object.ID+`"`)
	}
	header.Set("X-Content-Type-Options", "nosniff")
	// The response is scoped to the caller, so a shared browser profile
	// must not replay it for another user.
	header.Set("Cache-Control", "private, no-store")
	// ServeContent adds Accept-Ranges, serves byte ranges as 206, and keeps
	// the Content-Type set above because no name is given to sniff from.
	http.ServeContent(c.Response(), c.Request(), "", object.CreatedAt, reader)
	return nil
}

func mediaError(id string, err error) error {
	if errors.Is(err, mediastore.ErrNotFound) {
		return mediaNotFound(id)
	}
	return core.NewProviderError("media", http.StatusServiceUnavailable, "failed to read media object", err)
}

func mediaNotFound(id string) error {
	return core.NewNotFoundError("media object not found: " + id).WithCode("media_not_found")
}
