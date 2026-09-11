package pluginapi

// Enforcement is what a guardrail does with a finding, shared by the
// built-in guardrails so their block, respond, and warn behave alike. Read
// it from the conventional keys with [Config] and render a finding with
// [Enforcement.Enforce] or [Enforcement.Reject].
type Enforcement struct {
	// Action is [ActionBlock], [ActionRespond], or [ActionWarn]. A plugin
	// with actions of its own (replace, anonymize) handles those itself and
	// leaves the rest to Enforce.
	Action Action
	// Message is the error message for block, the audit note for warn, and
	// the assistant reply for respond when RespondText is empty.
	Message string
	// BlockStatus is the HTTP status for block; 0 is the phase default.
	BlockStatus int
	// RespondText is the assistant reply for respond, when it differs from
	// Message.
	RespondText string
}

// Enforce renders a finding as the configured action: a block error, a
// respond completion, or a warning, each with code and detail.
func (e Enforcement) Enforce(code string, detail any) Decision {
	if e.Action == ActionWarn {
		return Warn(code, e.Message, detail)
	}
	return e.Reject(code, detail)
}

// Reject renders a finding that must not pass: respond when that is the
// configured action, a block error otherwise, even when the action is warn.
func (e Enforcement) Reject(code string, detail any) Decision {
	if e.Action == ActionRespond {
		d := Respond(e.respondText())
		d.Code = code
		d.Detail = detail
		return d
	}
	d := Block(e.BlockStatus, code, e.Message)
	d.Detail = detail
	return d
}

func (e Enforcement) respondText() string {
	if e.RespondText != "" {
		return e.RespondText
	}
	return e.Message
}

// BlockStatusField is the conventional "block_status" form field, read
// with [Config.BlockStatus].
func BlockStatusField() Field {
	return Field{
		Key: "block_status", Label: "Block status", Input: InputNumber,
		Help:        "One HTTP status code between 400 and 599 that a blocked request returns, for example 403. Leave empty to use the phase default: 400 when the prompt is blocked, 502 when the response is blocked.",
		Placeholder: "phase default",
	}
}
