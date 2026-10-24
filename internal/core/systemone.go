package core

import "github.com/goccy/go-json"

// SystemOneRequest is the part of a System One decision request the gateway
// reads: the model it routes on and the state guardrails inspect. The rest of
// the body (the questions and their criteria) reaches the provider unchanged.
type SystemOneRequest struct {
	Model string `json:"model"`
	// State is the text or record the questions are asked about, as sent: a
	// JSON string in TypeSafe's examples, but any JSON value is forwarded.
	State json.RawMessage `json:"state,omitempty"`
}
