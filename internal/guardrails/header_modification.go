package guardrails

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// headerModificationCondition is one inbound-header predicate. All conditions
// of a rule must hold for its actions to apply.
type headerModificationCondition struct {
	// Header is the inbound header the predicate inspects.
	Header string `json:"header"`
	// Equals matches when any inbound value equals this string exactly.
	Equals *string `json:"equals,omitempty"`
	// Matches matches when any inbound value matches this RE2 regex.
	Matches *string `json:"matches,omitempty"`
	// Present requires the header to exist (true) or be absent (false).
	// Ignored when Equals or Matches is set; defaults to true otherwise.
	Present *bool `json:"present,omitempty"`
}

// headerModificationAction is one outbound-header change.
type headerModificationAction struct {
	// Action is "set" (replace/add the header) or "remove".
	Action string `json:"action"`
	// Header is the outbound header to change.
	Header string `json:"header"`
	// Value is the literal value for "set".
	Value string `json:"value,omitempty"`
	// FromHeader copies the first inbound value of this header for "set".
	// When the inbound header is absent the action is skipped.
	FromHeader string `json:"from_header,omitempty"`
}

type headerModificationDefinitionConfig struct {
	When    []headerModificationCondition `json:"when,omitempty"`
	Actions []headerModificationAction    `json:"actions"`
}

const (
	headerActionSet    = "set"
	headerActionRemove = "remove"
)

// headerFieldNameRE matches valid HTTP field names (RFC 9110 tokens).
var headerFieldNameRE = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

func decodeHeaderModificationDefinitionConfig(raw json.RawMessage) (headerModificationDefinitionConfig, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}

	var cfg headerModificationDefinitionConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return headerModificationDefinitionConfig{}, newValidationError("invalid header_modification config: "+err.Error(), err)
	}
	if decoder.More() {
		return headerModificationDefinitionConfig{}, newValidationError("invalid header_modification config: trailing data", nil)
	}

	for i := range cfg.When {
		condition, err := normalizeHeaderModificationCondition(cfg.When[i], i)
		if err != nil {
			return headerModificationDefinitionConfig{}, err
		}
		cfg.When[i] = condition
	}

	if len(cfg.Actions) == 0 {
		return headerModificationDefinitionConfig{}, newValidationError("header_modification requires at least one action", nil)
	}
	for i := range cfg.Actions {
		action, err := normalizeHeaderModificationAction(cfg.Actions[i], i)
		if err != nil {
			return headerModificationDefinitionConfig{}, err
		}
		cfg.Actions[i] = action
	}
	return cfg, nil
}

func normalizeHeaderModificationCondition(condition headerModificationCondition, index int) (headerModificationCondition, error) {
	header, err := normalizeRuleHeaderName(condition.Header)
	if err != nil {
		return headerModificationCondition{}, newValidationError(fmt.Sprintf("header_modification condition #%d: %s", index+1, err.Error()), nil)
	}
	condition.Header = header
	if condition.Equals != nil && condition.Matches != nil {
		return headerModificationCondition{}, newValidationError(fmt.Sprintf("header_modification condition #%d (%s): equals and matches are mutually exclusive", index+1, header), nil)
	}
	if (condition.Equals != nil || condition.Matches != nil) && condition.Present != nil && !*condition.Present {
		return headerModificationCondition{}, newValidationError(fmt.Sprintf("header_modification condition #%d (%s): present=false cannot be combined with equals or matches", index+1, header), nil)
	}
	if condition.Matches != nil {
		if _, err := regexp.Compile(*condition.Matches); err != nil {
			return headerModificationCondition{}, newValidationError(fmt.Sprintf("header_modification condition #%d (%s): invalid matches regex: %s", index+1, header, err.Error()), err)
		}
	}
	return condition, nil
}

func normalizeHeaderModificationAction(action headerModificationAction, index int) (headerModificationAction, error) {
	action.Action = strings.ToLower(strings.TrimSpace(action.Action))
	header, err := normalizeRuleHeaderName(action.Header)
	if err != nil {
		return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d: %s", index+1, err.Error()), nil)
	}
	action.Header = header

	switch action.Action {
	case headerActionSet:
		if action.Value != "" && action.FromHeader != "" {
			return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d (%s): value and from_header are mutually exclusive", index+1, header), nil)
		}
		if action.Value == "" && action.FromHeader == "" {
			return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d (%s): set requires value or from_header", index+1, header), nil)
		}
		if action.FromHeader != "" {
			source, err := normalizeRuleHeaderName(action.FromHeader)
			if err != nil {
				return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d (%s): from_header: %s", index+1, header, err.Error()), nil)
			}
			action.FromHeader = source
		}
	case headerActionRemove:
		if action.Value != "" || action.FromHeader != "" {
			return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d (%s): remove takes no value", index+1, header), nil)
		}
	case "":
		return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d (%s): action is required (set or remove)", index+1, header), nil)
	default:
		return headerModificationAction{}, newValidationError(fmt.Sprintf("header_modification action #%d (%s): unknown action %q (want set or remove)", index+1, header, action.Action), nil)
	}
	return action, nil
}

// normalizeRuleHeaderName canonicalizes a header name and rejects names a
// header rule may never touch: credentials and transport/framing headers.
func normalizeRuleHeaderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("header name is required")
	}
	if !headerFieldNameRE.MatchString(name) {
		return "", fmt.Errorf("invalid header name %q", name)
	}
	switch strings.ToLower(name) {
	case "accept-encoding", "content-encoding", "content-type":
		return "", fmt.Errorf("header %q controls payload encoding or media type and cannot be used in header rules", name)
	}
	if core.IsProtectedHeader(name) {
		return "", fmt.Errorf("header %q carries credentials or transport state and cannot be used in header rules", name)
	}
	return http.CanonicalHeaderKey(name), nil
}

type compiledHeaderCondition struct {
	header  string
	equals  *string
	matches *regexp.Regexp
	present bool
}

// HeaderModificationGuardrail is a workflow step that conditionally modifies
// outbound provider-request headers. It never touches messages: Process is
// the identity so the step composes with message guardrails in one pipeline.
type HeaderModificationGuardrail struct {
	name       string
	conditions []compiledHeaderCondition
	actions    []headerModificationAction
}

// NewHeaderModificationGuardrail compiles a validated definition config into
// an executable header-modification step.
func NewHeaderModificationGuardrail(name string, cfg headerModificationDefinitionConfig) (*HeaderModificationGuardrail, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("guardrail name is required")
	}

	conditions := make([]compiledHeaderCondition, 0, len(cfg.When))
	for _, condition := range cfg.When {
		compiled := compiledHeaderCondition{
			header:  condition.Header,
			equals:  condition.Equals,
			present: condition.Present == nil || *condition.Present,
		}
		if condition.Matches != nil {
			re, err := regexp.Compile(*condition.Matches)
			if err != nil {
				return nil, fmt.Errorf("compile matches regex for %s: %w", condition.Header, err)
			}
			compiled.matches = re
		}
		conditions = append(conditions, compiled)
	}

	return &HeaderModificationGuardrail{
		name:       name,
		conditions: conditions,
		actions:    append([]headerModificationAction(nil), cfg.Actions...),
	}, nil
}

// Name returns the guardrail's unique name.
func (g *HeaderModificationGuardrail) Name() string {
	if g == nil {
		return ""
	}
	return g.name
}

// Process is the identity: header steps do not modify messages.
func (g *HeaderModificationGuardrail) Process(_ context.Context, msgs []Message) ([]Message, error) {
	return msgs, nil
}

// HeaderMutation evaluates the rule against the inbound client headers and
// returns the outbound mutation, or nil when a condition does not hold.
func (g *HeaderModificationGuardrail) HeaderMutation(inbound http.Header) *core.HeaderMutation {
	if g == nil {
		return nil
	}
	for _, condition := range g.conditions {
		if !condition.holds(inbound) {
			return nil
		}
	}

	mutation := &core.HeaderMutation{}
	for _, action := range g.actions {
		switch action.Action {
		case headerActionSet:
			value := action.Value
			if action.FromHeader != "" {
				value = headerValue(inbound, action.FromHeader)
				if value == "" {
					continue
				}
			}
			if mutation.Set == nil {
				mutation.Set = make(map[string]string)
			}
			mutation.Set[action.Header] = value
			mutation.Remove = removeStringOnce(mutation.Remove, action.Header)
		case headerActionRemove:
			delete(mutation.Set, action.Header)
			mutation.Remove = appendStringOnce(mutation.Remove, action.Header)
		}
	}
	if mutation.IsZero() {
		return nil
	}
	return mutation
}

func (c compiledHeaderCondition) holds(inbound http.Header) bool {
	var values []string
	if inbound != nil {
		values = inbound.Values(c.header)
	}
	switch {
	case c.matches != nil:
		return slices.ContainsFunc(values, c.matches.MatchString)
	case c.equals != nil:
		return slices.Contains(values, *c.equals)
	case c.present:
		return len(values) > 0
	default:
		return len(values) == 0
	}
}

func headerValue(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	return h.Get(name)
}

func appendStringOnce(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func removeStringOnce(values []string, value string) []string {
	for i, existing := range values {
		if existing == value {
			return append(values[:i], values[i+1:]...)
		}
	}
	return values
}

// HeaderMutators returns the pipeline's header-mutating steps in execution
// order (ascending step order, registration order within a step).
func (p *Pipeline) HeaderMutators() []core.HeaderMutator {
	if p == nil {
		return nil
	}
	var mutators []core.HeaderMutator
	for _, group := range p.groups() {
		for _, e := range group {
			if mutator, ok := e.guardrail.(core.HeaderMutator); ok {
				mutators = append(mutators, mutator)
			}
		}
	}
	return mutators
}

func headerModificationDescriptor(name string, cfg headerModificationDefinitionConfig) RuleDescriptor {
	raw, err := json.Marshal(cfg)
	if err != nil {
		raw = nil
	}
	return RuleDescriptor{
		Name:    name,
		Type:    "header_modification",
		Content: string(raw),
	}
}

func summarizeHeaderModification(cfg headerModificationDefinitionConfig) string {
	parts := make([]string, 0, len(cfg.Actions))
	for _, action := range cfg.Actions {
		parts = append(parts, action.Action+" "+action.Header)
	}
	summary := strings.Join(parts, ", ")
	const maxLen = 72
	if len(summary) > maxLen {
		summary = summary[:maxLen-3] + "..."
	}
	switch len(cfg.When) {
	case 0:
		return "always • " + summary
	case 1:
		return "1 condition • " + summary
	default:
		return fmt.Sprintf("%d conditions • %s", len(cfg.When), summary)
	}
}
