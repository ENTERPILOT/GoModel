package server

import (
	"encoding/json"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

type passthroughService struct {
	responseHandler   PassthroughResponseHandler
	normalizeV1Prefix bool
}

func (s *passthroughService) ProviderPassthrough(c *echo.Context) error {
	provider := getPassthroughProvider(c)
	if provider == nil {
		return handleError(c, core.NewInvalidRequestError("passthrough provider not resolved", nil))
	}

	endpoint, err := extractPassthroughEndpoint(c, s.normalizeV1Prefix)
	if err != nil {
		return handleError(c, err)
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	upstreamHeaders := buildPassthroughHeaders(c.Request().Context(), c.Request().Header, requestID)
	resp, err := provider.Passthrough(c.Request().Context(), &core.PassthroughRequest{
		Method:   c.Request().Method,
		Endpoint: endpoint,
		Body:     c.Request().Body,
		Headers:  upstreamHeaders,
	})
	if err != nil {
		return handleError(c, err)
	}

	return s.responseHandler.Handle(c, requestID, resp)
}

// bestEffortModel attempts to extract the "model" field from a raw JSON request
// body. Returns an empty string if the body is not JSON or has no model field.
func bestEffortModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}
