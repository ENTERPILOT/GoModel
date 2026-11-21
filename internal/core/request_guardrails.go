package core

import (
	"strconv"
	"strings"

	"github.com/goccy/go-json"
)

// RequestGuardrailsField is the request body member that names the guardrails
// a client asks to run for one request. It is a gateway field: it is read at
// ingress and never forwarded to a provider.
const RequestGuardrailsField = "guardrails"

// TakeRequestGuardrails reads the guardrails member from a request's unknown
// fields and returns the fields without it. The member must be an array of
// guardrail names; null and an empty array name none. Names are trimmed and
// returned once each, in request order.
func TakeRequestGuardrails(fields UnknownJSONFields) ([]string, UnknownJSONFields, error) {
	if fields.IsEmpty() || !fields.HasAny(RequestGuardrailsField) {
		return nil, fields, nil
	}
	names, err := parseRequestGuardrails(fields.Lookup(RequestGuardrailsField))
	if err != nil {
		return nil, fields, err
	}
	return names, fields.Without(RequestGuardrailsField), nil
}

// parseRequestGuardrails decodes the value of a guardrails request member.
func parseRequestGuardrails(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || IsJSONNull(raw) {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, invalidRequestGuardrails("guardrails must be an array of guardrail names")
	}
	names := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			return nil, invalidRequestGuardrails("guardrails must not contain an empty name")
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

// NewRequestGuardrailUnavailableError is the error for a request that names a
// guardrail it may not select. An unknown name and a guardrail the matched
// workflow does not offer read the same, so the answer does not reveal which
// guardrails exist.
func NewRequestGuardrailUnavailableError(name string) *GatewayError {
	return invalidRequestGuardrails("guardrail " + strconv.Quote(name) + " cannot be selected for this request").
		WithCode("guardrail_not_available")
}

func invalidRequestGuardrails(message string) *GatewayError {
	return NewInvalidRequestError(message, nil).WithParam(RequestGuardrailsField)
}
