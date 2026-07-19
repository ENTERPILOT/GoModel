// Package headerpolicy owns reusable outbound request-header policies.
package headerpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/validation"
)

const (
	ActionSet    = "set"
	ActionRemove = "remove"
)

// ErrNotFound indicates that a requested policy definition does not exist.
var ErrNotFound = errors.New("header policy not found")

// Condition is one predicate over an inbound request header.
type Condition struct {
	Header  string  `json:"header" bson:"header"`
	Equals  *string `json:"equals,omitempty" bson:"equals,omitempty"`
	Matches *string `json:"matches,omitempty" bson:"matches,omitempty"`
	Present *bool   `json:"present,omitempty" bson:"present,omitempty"`
}

// Action is one outbound request-header mutation.
type Action struct {
	Action     string  `json:"action" bson:"action"`
	Header     string  `json:"header" bson:"header"`
	Value      *string `json:"value,omitempty" bson:"value,omitempty"`
	FromHeader string  `json:"from_header,omitempty" bson:"from_header,omitempty"`
}

// Definition is one persisted, reusable outbound header policy. The policy
// fields are intentionally top-level in the API instead of hiding behind a
// guardrail type/config union.
type Definition struct {
	Name        string      `json:"name" bson:"name"`
	Description string      `json:"description,omitempty" bson:"description,omitempty"`
	Methods     []string    `json:"methods,omitempty" bson:"methods,omitempty"`
	Paths       []string    `json:"paths,omitempty" bson:"paths,omitempty"`
	When        []Condition `json:"when,omitempty" bson:"when,omitempty"`
	Actions     []Action    `json:"actions" bson:"actions"`
	CreatedAt   time.Time   `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at" bson:"updated_at"`
}

// View is the admin-facing representation of a policy.
type View struct {
	Definition
	Summary string `json:"summary,omitempty"`
}

// Reference binds a named policy to an ordered workflow step.
type Reference struct {
	Ref  string
	Step int
}

// Store persists header-policy definitions.
type Store interface {
	List(ctx context.Context) ([]Definition, error)
	Get(ctx context.Context, name string) (*Definition, error)
	Upsert(ctx context.Context, definition Definition) error
	UpsertMany(ctx context.Context, definitions []Definition) error
	Delete(ctx context.Context, name string) error
	Close() error
}

// Catalog resolves workflow references into executable policies.
type Catalog interface {
	Names() []string
	BuildHeaderPolicies(steps []Reference) ([]core.HeaderPolicy, error)
}

// ValidationError identifies invalid policy authoring input.
type ValidationError = validation.Error

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// IsValidationError reports whether err is an authoring validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

var headerFieldNameRE = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

func normalizeDefinition(def Definition) (Definition, error) {
	def.Name = strings.TrimSpace(def.Name)
	def.Description = strings.TrimSpace(def.Description)
	if def.Name == "" {
		return Definition{}, newValidationError("header policy name is required", nil)
	}
	if strings.Contains(def.Name, "/") {
		return Definition{}, newValidationError("header policy name cannot contain '/'", nil)
	}
	var err error
	def.Methods, err = normalizeMethods(def.Methods)
	if err != nil {
		return Definition{}, err
	}
	def.Paths, err = normalizePaths(def.Paths)
	if err != nil {
		return Definition{}, err
	}
	for i := range def.When {
		def.When[i], err = normalizeCondition(def.When[i], i)
		if err != nil {
			return Definition{}, err
		}
	}
	if len(def.Actions) == 0 {
		return Definition{}, newValidationError("header policy requires at least one action", nil)
	}
	for i := range def.Actions {
		def.Actions[i], err = normalizeAction(def.Actions[i], i)
		if err != nil {
			return Definition{}, err
		}
	}
	return def, nil
}

func normalizeMethods(methods []string) ([]string, error) {
	result := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != "" && !headerFieldNameRE.MatchString(method) {
			return nil, newValidationError("invalid header policy HTTP method: "+method, nil)
		}
		if method != "" && !slices.Contains(result, method) {
			result = append(result, method)
		}
	}
	return result, nil
}

func normalizePaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			return nil, newValidationError("header policy path must start with '/': "+path, nil)
		}
		if strings.Count(path, "*") > 1 || (strings.Contains(path, "*") && !strings.HasSuffix(path, "*")) {
			return nil, newValidationError("header policy path wildcard is only allowed as a trailing '*': "+path, nil)
		}
		if !slices.Contains(result, path) {
			result = append(result, path)
		}
	}
	return result, nil
}

func normalizeCondition(condition Condition, index int) (Condition, error) {
	header, err := normalizeRuleHeaderName(condition.Header)
	if err != nil {
		return Condition{}, newValidationError(fmt.Sprintf("header policy condition #%d: %s", index+1, err.Error()), nil)
	}
	condition.Header = header
	if condition.Equals != nil && condition.Matches != nil {
		return Condition{}, newValidationError(fmt.Sprintf("header policy condition #%d (%s): equals and matches are mutually exclusive", index+1, header), nil)
	}
	if (condition.Equals != nil || condition.Matches != nil) && condition.Present != nil && !*condition.Present {
		return Condition{}, newValidationError(fmt.Sprintf("header policy condition #%d (%s): present=false cannot be combined with equals or matches", index+1, header), nil)
	}
	if condition.Matches != nil {
		if _, err := regexp.Compile(*condition.Matches); err != nil {
			return Condition{}, newValidationError(fmt.Sprintf("header policy condition #%d (%s): invalid matches regex: %s", index+1, header, err.Error()), err)
		}
	}
	return condition, nil
}

func normalizeAction(action Action, index int) (Action, error) {
	action.Action = strings.ToLower(strings.TrimSpace(action.Action))
	header, err := normalizeRuleHeaderName(action.Header)
	if err != nil {
		return Action{}, newValidationError(fmt.Sprintf("header policy action #%d: %s", index+1, err.Error()), nil)
	}
	action.Header = header
	action.FromHeader = strings.TrimSpace(action.FromHeader)
	switch action.Action {
	case ActionSet:
		if action.Value != nil && action.FromHeader != "" {
			return Action{}, newValidationError(fmt.Sprintf("header policy action #%d (%s): value and from_header are mutually exclusive", index+1, header), nil)
		}
		if action.Value == nil && action.FromHeader == "" {
			return Action{}, newValidationError(fmt.Sprintf("header policy action #%d (%s): set requires value or from_header", index+1, header), nil)
		}
		if action.FromHeader != "" {
			action.FromHeader, err = normalizeRuleHeaderName(action.FromHeader)
			if err != nil {
				return Action{}, newValidationError(fmt.Sprintf("header policy action #%d (%s): from_header: %s", index+1, header, err.Error()), nil)
			}
		}
	case ActionRemove:
		if action.Value != nil || action.FromHeader != "" {
			return Action{}, newValidationError(fmt.Sprintf("header policy action #%d (%s): remove takes no value", index+1, header), nil)
		}
	case "":
		return Action{}, newValidationError(fmt.Sprintf("header policy action #%d (%s): action is required (set or remove)", index+1, header), nil)
	default:
		return Action{}, newValidationError(fmt.Sprintf("header policy action #%d (%s): unknown action %q (want set or remove)", index+1, header, action.Action), nil)
	}
	return action, nil
}

func normalizeRuleHeaderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("header name is required")
	}
	if !headerFieldNameRE.MatchString(name) {
		return "", fmt.Errorf("invalid header name %q", name)
	}
	// These names are covered by IsProtectedHeader too, but get a specific
	// authoring error because overriding them breaks body parsing or transport
	// decompression rather than exposing credentials.
	switch strings.ToLower(name) {
	case "accept-encoding", "content-encoding", "content-type":
		return "", fmt.Errorf("header %q controls payload encoding or media type and cannot be used in header policies", name)
	}
	if core.IsProtectedHeader(name) {
		return "", fmt.Errorf("header %q carries credentials or transport state and cannot be used in header policies", name)
	}
	return http.CanonicalHeaderKey(name), nil
}

// ViewFromDefinition projects a definition with a compact action summary.
func ViewFromDefinition(def Definition) View {
	return View{Definition: cloneDefinition(def), Summary: summarize(def)}
}

func summarize(def Definition) string {
	parts := make([]string, 0, len(def.Actions))
	for _, action := range def.Actions {
		parts = append(parts, action.Action+" "+action.Header)
	}
	summary := strings.Join(parts, ", ")
	const maxLen = 72
	if len(summary) > maxLen {
		summary = summary[:maxLen-3] + "..."
	}
	switch len(def.When) {
	case 0:
		return "always • " + summary
	case 1:
		return "1 condition • " + summary
	default:
		return fmt.Sprintf("%d conditions • %s", len(def.When), summary)
	}
}

func cloneDefinition(def Definition) Definition {
	copy := def
	copy.Methods = append([]string(nil), def.Methods...)
	copy.Paths = append([]string(nil), def.Paths...)
	copy.When = append([]Condition(nil), def.When...)
	copy.Actions = append([]Action(nil), def.Actions...)
	for i := range copy.When {
		if copy.When[i].Equals != nil {
			value := *copy.When[i].Equals
			copy.When[i].Equals = &value
		}
		if copy.When[i].Matches != nil {
			value := *copy.When[i].Matches
			copy.When[i].Matches = &value
		}
		if copy.When[i].Present != nil {
			value := *copy.When[i].Present
			copy.When[i].Present = &value
		}
	}
	for i := range copy.Actions {
		if copy.Actions[i].Value != nil {
			value := *copy.Actions[i].Value
			copy.Actions[i].Value = &value
		}
	}
	return copy
}

type legacyConfig struct {
	Methods   []string    `json:"methods,omitempty"`
	Endpoints []string    `json:"endpoints,omitempty"`
	When      []Condition `json:"when,omitempty"`
	Actions   []Action    `json:"actions"`
}

// DefinitionFromLegacy converts a historical guardrail header_modification
// payload into the dedicated header-policy definition shape.
func DefinitionFromLegacy(name, description string, raw json.RawMessage) (Definition, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	var legacy legacyConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Definition{}, newValidationError("invalid legacy header_modification config: "+err.Error(), err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Definition{}, newValidationError("invalid legacy header_modification config: trailing data", nil)
	}
	return normalizeDefinition(Definition{
		Name: name, Description: description, Methods: legacy.Methods,
		Paths: legacy.Endpoints, When: legacy.When, Actions: legacy.Actions,
	})
}

// NormalizeLegacyConfig validates and canonicalizes a historical config while
// it remains readable from the guardrail definition store.
func NormalizeLegacyConfig(raw json.RawMessage) (json.RawMessage, error) {
	def, err := DefinitionFromLegacy("legacy", "", raw)
	if err != nil {
		return nil, err
	}
	legacy := legacyConfig{Methods: def.Methods, Endpoints: def.Paths, When: def.When, Actions: def.Actions}
	return json.Marshal(legacy)
}
