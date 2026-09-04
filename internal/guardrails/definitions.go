package guardrails

import (
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// Definition is one persisted reusable guardrail instance.
type Definition struct {
	Name        string          `json:"name" bson:"name"`
	Type        string          `json:"type" bson:"type"`
	Description string          `json:"description,omitempty" bson:"description,omitempty"`
	UserPath    string          `json:"user_path,omitempty" bson:"user_path,omitempty"`
	Config      json.RawMessage `json:"config" bson:"config"`
	// FailMode is "closed" or "open"; empty selects the phase default.
	FailMode string `json:"fail_mode,omitempty" bson:"fail_mode,omitempty"`
	// TimeoutMS bounds every hook call of the instance; zero means none.
	TimeoutMS int       `json:"timeout_ms,omitempty" bson:"timeout_ms,omitempty"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// View is the admin-facing representation of a persisted guardrail.
type View struct {
	Definition
	// Phases lists the hook phases the instance's plugin implements.
	Phases  []string `json:"phases,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

// ViewFromDefinition projects one guardrail definition into its admin-facing
// view without catalog-derived fields (phases, summary).
func ViewFromDefinition(def Definition) View {
	return View{Definition: cloneDefinition(def)}
}

// TypeOption is one allowed option for a typed guardrail config field.
type TypeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// TypeField describes one UI field for a guardrail type.
type TypeField struct {
	Key         string       `json:"key"`
	Label       string       `json:"label"`
	Input       string       `json:"input"`
	Required    bool         `json:"required"`
	Help        string       `json:"help,omitempty"`
	Placeholder string       `json:"placeholder,omitempty"`
	Options     []TypeOption `json:"options,omitempty"`
	Default     any          `json:"default,omitempty"`
	Scope       string       `json:"scope,omitempty"`
}

// TypeDefinition describes one supported guardrail type and its config schema.
type TypeDefinition struct {
	Type        string          `json:"type"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Defaults    json.RawMessage `json:"defaults"`
	Fields      []TypeField     `json:"fields"`
	Phases      []string        `json:"phases"`
	Source      string          `json:"source"`
	Mutates     bool            `json:"mutates"`
}

// TypeFieldsFromSchema converts the schema fields of one scope.
func TypeFieldsFromSchema(schema []pluginapi.Field, scope pluginapi.FieldScope) []TypeField {
	fields := make([]TypeField, 0, len(schema))
	for _, field := range schema {
		if field.Scope != scope {
			continue
		}
		input := string(field.Input)
		if input == "" {
			input = string(pluginapi.InputText)
		}
		converted := TypeField{
			Key:         field.Key,
			Label:       field.Label,
			Input:       input,
			Required:    field.Required,
			Help:        field.Help,
			Placeholder: field.Placeholder,
			Default:     field.Default,
			Scope:       string(field.Scope),
		}
		for _, option := range field.Options {
			converted.Options = append(converted.Options, TypeOption{Value: option.Value, Label: option.Label})
		}
		fields = append(fields, converted)
	}
	return fields
}

// typeDefinitionFromEntry renders one catalog entry for the guardrail editor.
func typeDefinitionFromEntry(entry plugins.Entry) TypeDefinition {
	return TypeDefinition{
		Type:        entry.Name,
		Label:       typeLabel(entry.Name),
		Description: entry.Manifest.Description,
		Defaults:    plugins.SchemaDefaults(entry.Manifest.ConfigSchema),
		Fields:      TypeFieldsFromSchema(entry.Manifest.ConfigSchema, pluginapi.ScopeInstance),
		Phases:      phaseNames(entry.Kinds),
		Source:      typeSource(entry.Source),
		Mutates:     entry.Manifest.Mutates,
	}
}

func typeSource(source plugins.Source) string {
	switch source {
	case plugins.SourceBuiltin, plugins.SourceRegistered:
		return string(source)
	default:
		return "file"
	}
}

// typeLabel turns a manifest name into a title: "llm_based_altering" becomes
// "LLM Based Altering".
func typeLabel(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
	for i, word := range words {
		switch strings.ToLower(word) {
		case "llm", "pii", "api", "url", "http", "json":
			words[i] = strings.ToUpper(word)
		default:
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func phaseNames(kinds []pluginapi.Kind) []string {
	phases := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if plugins.IsPhaseKind(kind) {
			phases = append(phases, string(kind))
		}
	}
	return phases
}

func hasPhase(kinds []pluginapi.Kind) bool {
	return len(phaseNames(kinds)) > 0
}

// normalizeDefinitionIdentity trims and validates the catalog-independent
// fields: name, type, user path, fail mode and timeout. Stores call it before
// persisting; the service additionally validates the config.
func normalizeDefinitionIdentity(def Definition) (Definition, error) {
	def.Name = strings.TrimSpace(def.Name)
	def.Type = normalizeDefinitionType(def.Type)
	def.Description = strings.TrimSpace(def.Description)
	userPath, err := core.NormalizeUserPath(def.UserPath)
	if err != nil {
		return Definition{}, newValidationError("invalid user_path", err)
	}
	def.UserPath = userPath

	if def.Name == "" {
		return Definition{}, newValidationError("guardrail name is required", nil)
	}
	if strings.Contains(def.Name, "/") {
		return Definition{}, newValidationError("guardrail name cannot contain '/'", nil)
	}
	if def.Type == "" {
		return Definition{}, newValidationError("guardrail type is required", nil)
	}
	failMode, err := plugins.ParseFailMode(def.FailMode)
	if err != nil {
		return Definition{}, newValidationError(err.Error(), err)
	}
	def.FailMode = string(failMode)
	if def.TimeoutMS < 0 {
		return Definition{}, newValidationError("timeout_ms cannot be negative", nil)
	}
	if len(strings.TrimSpace(string(def.Config))) == 0 {
		def.Config = json.RawMessage(`{}`)
	}
	return def, nil
}

func normalizeDefinitionType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "system-prompt":
		return "system_prompt"
	case "llm-based-altering":
		return "llm_based_altering"
	default:
		return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "plugin:")
	}
}

func cloneDefinition(def Definition) Definition {
	cloned := def
	if len(def.Config) > 0 {
		cloned.Config = append(json.RawMessage(nil), def.Config...)
	}
	return cloned
}

func instanceSpec(def Definition) plugins.InstanceSpec {
	return plugins.InstanceSpec{
		Name:     def.Name,
		Config:   def.Config,
		FailMode: plugins.FailMode(def.FailMode),
		Timeout:  time.Duration(def.TimeoutMS) * time.Millisecond,
	}
}
