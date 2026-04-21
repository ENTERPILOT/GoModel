package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type passthroughService struct {
	logger            auditlog.LoggerInterface
	responseHandler   PassthroughResponseHandler
	normalizeV1Prefix bool
}

func (s *passthroughService) ProviderPassthrough(c *echo.Context) error {
	instanceName := getPassthroughInstanceName(c)
	providerType := getPassthroughProviderType(c)
	provider := getPassthroughProvider(c)
	requestID := getPassthroughRequestID(c)

	if provider == nil {
		return handleError(c, core.NewInvalidRequestError("passthrough provider not resolved", nil))
	}

	endpoint, err := extractPassthroughEndpoint(c, s.normalizeV1Prefix)
	if err != nil {
		return handleError(c, err)
	}

	body, bodyErr := readAndRestoreBody(c.Request())
	if bodyErr != nil {
		return handleError(c, core.NewInvalidRequestError("failed to read request body", bodyErr))
	}

	upstreamHeaders := buildPassthroughHeaders(c.Request().Context(), c.Request().Header, requestID)
	resp, err := provider.Passthrough(c.Request().Context(), &core.PassthroughRequest{
		Method:   c.Request().Method,
		Endpoint: endpoint,
		Body:     c.Request().Body,
		Headers:  upstreamHeaders,
	})
	if err != nil {
		recordPassthroughAudit(s.logger, PassthroughAuditEntry{
			InstanceName: instanceName,
			ProviderType: providerType,
			Method:       c.Request().Method,
			Path:         c.Request().URL.Path,
			Endpoint:     endpoint,
			RequestID:    requestID,
			StatusCode:   passthroughAuditHTTPStatus(err),
			Timestamp:    time.Now().UTC(),
			Model:        bestEffortModel(body),
			ClientIP:     c.RealIP(),
		})
		return handleError(c, err)
	}

	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}

	recordPassthroughAudit(s.logger, PassthroughAuditEntry{
		InstanceName: instanceName,
		ProviderType: providerType,
		Method:       c.Request().Method,
		Path:         c.Request().URL.Path,
		Endpoint:     endpoint,
		RequestID:    requestID,
		StatusCode:   statusCode,
		Timestamp:    time.Now().UTC(),
		Model:        bestEffortModel(body),
		ClientIP:     c.RealIP(),
	})

	return s.responseHandler.Handle(c, requestID, resp)
}

func passthroughAuditHTTPStatus(err error) int {
	var gw *core.GatewayError
	if errors.As(err, &gw) && gw != nil {
		return gw.HTTPStatusCode()
	}
	return http.StatusBadGateway
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
