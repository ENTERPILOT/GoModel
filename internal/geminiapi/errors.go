package geminiapi

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
)

// ErrorFromGateway converts a gateway error into an HTTP status code and the
// Gemini error envelope. A nil error is reported as a generic INTERNAL error.
func ErrorFromGateway(err *core.GatewayError) (int, ErrorResponse) {
	if err == nil {
		return http.StatusInternalServerError, NewErrorResponse(http.StatusInternalServerError, "an unexpected error occurred")
	}
	status := err.HTTPStatusCode()
	return status, NewErrorResponse(status, err.Message)
}

// NewErrorResponse builds the Gemini error envelope for an HTTP status.
func NewErrorResponse(status int, message string) ErrorResponse {
	return ErrorResponse{Error: ErrorObject{Code: status, Message: message, Status: rpcStatus(status)}}
}

// rpcStatus names the canonical gRPC status Google APIs report for an HTTP
// status code (google.rpc.Code). Gemini SDKs branch on the HTTP code and show
// this name alongside the message.
func rpcStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case http.StatusNotImplemented:
		return "UNIMPLEMENTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	}
	if status >= http.StatusInternalServerError {
		return "INTERNAL"
	}
	return "INVALID_ARGUMENT"
}
