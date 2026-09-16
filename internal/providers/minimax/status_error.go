package minimax

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxStatusMessageLength bounds the MiniMax diagnostic copied into a gateway
// error. GatewayError.Message is returned to API clients, so an oversized
// upstream status_msg must not flow through unbounded.
const maxStatusMessageLength = 2048

// statusError maps a MiniMax base_resp status code to a gateway error.
// MiniMax reports failures as HTTP 200 with a non-zero base_resp.status_code,
// so caller mistakes (invalid parameters, auth, balance, rate limits) must be
// surfaced with their real meaning rather than a blanket 502. operation names
// the request in the message ("speech", "image"), statusMessage carries
// MiniMax's own diagnostic, and body is the raw upstream response retained for
// auditing.
func statusError(operation string, statusCode int, statusMessage string, body []byte) error {
	message := fmt.Sprintf("minimax %s request failed (status %d)", operation, statusCode)
	if statusMessage = truncateStatusMessage(strings.TrimSpace(statusMessage)); statusMessage != "" {
		message += ": " + statusMessage
	}
	var gatewayErr *core.GatewayError
	switch statusCode {
	case 1002, 1039, 2045, 2056: // rate limit / token limit / rate growth limit / usage limit
		gatewayErr = core.NewRateLimitError("minimax", message)
	case 1004, 2049: // not authorized / invalid API key
		gatewayErr = core.NewAuthenticationError("minimax", message)
	case 1008: // insufficient balance
		gatewayErr = core.NewProviderError("minimax", http.StatusPaymentRequired, message, nil)
	case 1026, 1042, 2013, 20132: // sensitive input / invisible characters / invalid params / invalid voice_id
		gatewayErr = core.NewInvalidRequestError(message, nil)
		gatewayErr.Provider = "minimax"
	default:
		gatewayErr = core.NewProviderError("minimax", http.StatusBadGateway, message, nil)
	}
	return gatewayErr.WithResponseBody(body)
}

// truncateStatusMessage cuts message to maxStatusMessageLength bytes. The cut
// backs off to a rune boundary so a multi-byte rune (common in MiniMax's
// Chinese diagnostics) is never split into invalid UTF-8.
func truncateStatusMessage(message string) string {
	if len(message) <= maxStatusMessageLength {
		return message
	}
	cut := maxStatusMessageLength
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut]
}
