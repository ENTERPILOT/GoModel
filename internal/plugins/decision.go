package plugins

import (
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// Codes used when a plugin does not set its own.
const (
	CodePluginFailure = "plugin_failure"
	CodeBlocked       = "guardrail_blocked"
	CodeWarn          = "guardrail_warning"
)

// Severity ranks decisions so concurrent results merge deterministically:
// block > respond > warn > allow.
func Severity(action pluginapi.Action) int {
	switch action {
	case pluginapi.ActionBlock:
		return 3
	case pluginapi.ActionRespond:
		return 2
	case pluginapi.ActionWarn:
		return 1
	default:
		return 0
	}
}

// NormalizeDecision maps the zero action to allow.
func NormalizeDecision(d pluginapi.Decision) pluginapi.Decision {
	if d.Action == "" {
		d.Action = pluginapi.ActionAllow
	}
	if d.Action == pluginapi.ActionRespond && d.Response == nil {
		d.Response = &pluginapi.Completion{}
	}
	return d
}

// MergeDecision returns the more severe of two decisions; on a tie the first
// one wins so step order decides.
func MergeDecision(current, next pluginapi.Decision) pluginapi.Decision {
	if Severity(next.Action) > Severity(current.Action) {
		return next
	}
	return current
}

// BlockError renders a block decision as the gateway error the client sees.
// defaultStatus is used when the decision has no status (400 for request
// phases, 502 for response phases) or one outside 400-599: a plugin may
// hand back any integer, and net/http panics on a status it cannot write.
func BlockError(d pluginapi.Decision, defaultStatus int) *core.GatewayError {
	status := d.Status
	if !blockStatusOK(status) {
		status = defaultStatus
	}
	if !blockStatusOK(status) {
		status = http.StatusBadRequest
	}
	message := strings.TrimSpace(d.Message)
	if message == "" {
		message = "request blocked by guardrail"
	}
	code := strings.TrimSpace(d.Code)
	if code == "" {
		code = CodeBlocked
	}
	var gatewayErr *core.GatewayError
	if status >= http.StatusInternalServerError {
		// The decision came from a plugin, not from an upstream provider, so
		// the error type names the gateway rather than a provider.
		gatewayErr = core.NewInternalErrorWithStatus(status, message, nil)
	} else {
		gatewayErr = core.NewInvalidRequestErrorWithStatus(status, message, nil)
	}
	return gatewayErr.WithCode(code)
}

func blockStatusOK(status int) bool {
	return status >= http.StatusBadRequest && status <= 599
}

// FailureError renders a fail-closed plugin error: HTTP 500 with type
// internal_error and code plugin_failure. No provider is involved, so the
// error is attributed to the gateway. The instance name stays out of the
// client message.
func FailureError(err error) *core.GatewayError {
	return core.NewInternalErrorWithStatus(http.StatusInternalServerError, "a request plugin failed", err).WithCode(CodePluginFailure)
}

// WarnHeaderValue renders the X-GoModel-Guardrail header for a warn decision.
func WarnHeaderValue(d pluginapi.Decision) string {
	code := strings.TrimSpace(d.Code)
	if code == "" {
		code = CodeWarn
	}
	return "warn; code=" + code
}

// GuardrailHeader is the response header carrying warn decisions.
const GuardrailHeader = "X-GoModel-Guardrail"
