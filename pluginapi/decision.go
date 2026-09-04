package pluginapi

// Action is what a hook asks GoModel to do with the request or response.
type Action string

const (
	// ActionAllow continues, with any edits already applied to the Exchange.
	ActionAllow Action = "allow"
	// ActionBlock rejects the request with Decision.Status, Code, and Message
	// rendered in the endpoint's native error dialect.
	ActionBlock Action = "block"
	// ActionRespond short-circuits: Decision.Response is sent to the client as
	// the completion with HTTP 200 (as a single-chunk stream when streaming).
	ActionRespond Action = "respond"
	// ActionWarn continues and records Decision.Detail in the audit trail and
	// the X-GoModel-Guardrail response headers.
	ActionWarn Action = "warn"
)

// Decision is the result of a synchronous hook.
type Decision struct {
	// Action selects what happens next. The zero value is treated as
	// [ActionAllow].
	Action Action
	// Status is the HTTP status for [ActionBlock]. Zero means the phase
	// default: 400 in request phases, 502 in response phases.
	Status int
	// Code is a machine-readable reason such as "content_policy".
	Code string
	// Message is the human-readable reason sent to the client on block.
	Message string
	// Response is the synthetic completion for [ActionRespond].
	Response *Completion
	// Detail is a JSON-serializable summary stored in the audit trail. It
	// must not contain secrets.
	Detail any
}

// Allow returns a Decision that lets the exchange continue.
func Allow() Decision {
	return Decision{Action: ActionAllow}
}

// Block returns a Decision that rejects the exchange with the given HTTP
// status (0 for the phase default), machine-readable code, and message.
func Block(status int, code, message string) Decision {
	return Decision{Action: ActionBlock, Status: status, Code: code, Message: message}
}

// Respond returns a Decision that answers the request with a one-choice
// assistant completion containing text, keeping agent loops alive instead of
// surfacing an error.
func Respond(text string) Decision {
	return Decision{
		Action: ActionRespond,
		Response: &Completion{
			Choices: []Choice{{
				Index:        0,
				Message:      TextMessage(RoleAssistant, text),
				FinishReason: "stop",
			}},
		},
	}
}

// Warn returns a Decision that lets the exchange continue while recording
// code, message, and detail in the audit trail and response headers.
func Warn(code, message string, detail any) Decision {
	return Decision{Action: ActionWarn, Code: code, Message: message, Detail: detail}
}

// Blocks reports whether the decision stops the exchange from reaching the
// provider or the client as-is: [ActionBlock] or [ActionRespond].
func (d Decision) Blocks() bool {
	return d.Action == ActionBlock || d.Action == ActionRespond
}
