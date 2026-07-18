package headerpolicy

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

type compiledCondition struct {
	header  string
	equals  *string
	matches *regexp.Regexp
	present bool
}

// Policy is an executable outbound request-header policy.
type Policy struct {
	name       string
	methods    []string
	paths      []string
	conditions []compiledCondition
	actions    []Action
}

// NewPolicy compiles one validated definition.
func NewPolicy(def Definition) (*Policy, error) {
	def, err := normalizeDefinition(def)
	if err != nil {
		return nil, err
	}
	conditions := make([]compiledCondition, 0, len(def.When))
	for _, condition := range def.When {
		compiled := compiledCondition{
			header: condition.Header, equals: condition.Equals,
			present: condition.Present == nil || *condition.Present,
		}
		if condition.Matches != nil {
			compiled.matches, err = regexp.Compile(*condition.Matches)
			if err != nil {
				return nil, fmt.Errorf("compile matches regex for %s: %w", condition.Header, err)
			}
		}
		conditions = append(conditions, compiled)
	}
	return &Policy{
		name: def.Name, methods: append([]string(nil), def.Methods...),
		paths: append([]string(nil), def.Paths...), conditions: conditions,
		actions: append([]Action(nil), def.Actions...),
	}, nil
}

// Name returns the reusable policy name.
func (p *Policy) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

// ResolveHeaderPlan evaluates the policy against immutable request metadata.
func (p *Policy) ResolveHeaderPlan(input core.HeaderPolicyInput) *core.HeaderPlan {
	if p == nil {
		return nil
	}
	if len(p.methods) > 0 && !slices.Contains(p.methods, strings.ToUpper(strings.TrimSpace(input.Method))) {
		return nil
	}
	if len(p.paths) > 0 && !slices.ContainsFunc(p.paths, func(selector string) bool {
		if prefix, ok := strings.CutSuffix(selector, "*"); ok {
			return strings.HasPrefix(input.Path, prefix)
		}
		return input.Path == selector
	}) {
		return nil
	}
	for _, condition := range p.conditions {
		if !condition.holds(input.Headers) {
			return nil
		}
	}
	plan := &core.HeaderPlan{}
	for _, action := range p.actions {
		switch action.Action {
		case ActionSet:
			value := ""
			sensitive := false
			if action.Value != nil {
				value = *action.Value
			} else {
				values := input.Headers.Values(action.FromHeader)
				if len(values) == 0 {
					continue
				}
				value = values[0]
				sensitive = core.ShouldRedactHeader(action.FromHeader)
			}
			if plan.Set == nil {
				plan.Set = make(map[string]string)
			}
			plan.Set[action.Header] = value
			plan.Remove = removeOnce(plan.Remove, action.Header)
			if sensitive {
				plan.SensitiveSet = appendOnce(plan.SensitiveSet, action.Header)
			} else {
				plan.SensitiveSet = removeOnce(plan.SensitiveSet, action.Header)
			}
		case ActionRemove:
			delete(plan.Set, action.Header)
			plan.SensitiveSet = removeOnce(plan.SensitiveSet, action.Header)
			plan.Remove = appendOnce(plan.Remove, action.Header)
		}
	}
	if plan.IsZero() {
		return nil
	}
	return plan
}

func (c compiledCondition) holds(headers http.Header) bool {
	values := headers.Values(c.header)
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

func appendOnce(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func removeOnce(values []string, value string) []string {
	for i, existing := range values {
		if existing == value {
			return append(values[:i], values[i+1:]...)
		}
	}
	return values
}
