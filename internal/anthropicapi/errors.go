package anthropicapi

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
)

// ErrorFromGateway converts a gateway error into an HTTP status code and the
// Anthropic error envelope. A nil error is reported as a generic api_error.
func ErrorFromGateway(err *core.GatewayError) (int, ErrorResponse) {
	if err == nil {
		return http.StatusInternalServerError, newErrorResponse("api_error", "an unexpected error occurred")
	}
	return err.HTTPStatusCode(), newErrorResponse(anthropicErrorType(err), err.Message)
}

func newErrorResponse(errType, message string) ErrorResponse {
	return ErrorResponse{
		Type:  "error",
		Error: ErrorObject{Type: errType, Message: message},
	}
}

// anthropicErrorType maps a core error type onto the nearest Anthropic error
// type. The status code is preserved separately by the caller.
func anthropicErrorType(err *core.GatewayError) string {
	switch err.Type {
	case core.ErrorTypeInvalidRequest:
		if err.StatusCode == http.StatusRequestEntityTooLarge {
			return "request_too_large"
		}
		return "invalid_request_error"
	case core.ErrorTypeAuthentication:
		if err.StatusCode == http.StatusForbidden {
			return "permission_error"
		}
		return "authentication_error"
	case core.ErrorTypeNotFound:
		return "not_found_error"
	case core.ErrorTypeRateLimit:
		return "rate_limit_error"
	case core.ErrorTypeProvider:
		if err.StatusCode == http.StatusServiceUnavailable {
			return "overloaded_error"
		}
		return "api_error"
	default:
		return "api_error"
	}
}
