package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// currentSchemaVersion is the payload schema written by the dashboard.
// Version 1 payloads (guardrails: [{ref, step}]) stay accepted and are stored
// as-is; their steps compile as prompt-phase steps.
const currentSchemaVersion = 2

// Phase names accepted in a version 2 step.
const (
	PhasePrompt   = "prompt"
	PhaseResponse = "response"
	PhaseStream   = "stream"
)

// Scope identifies the request selector a persisted workflow applies to.
// Provider stores the configured provider instance name, not the provider type.
type Scope struct {
	Provider string `json:"-" bson:"scope_provider,omitempty"`
	Model    string `json:"scope_model,omitempty" bson:"scope_model,omitempty"`
	UserPath string `json:"scope_user_path,omitempty" bson:"scope_user_path,omitempty"`
}

type scopeJSON struct {
	ProviderName   string `json:"scope_provider_name,omitempty"`
	LegacyProvider string `json:"scope_provider,omitempty"`
	Model          string `json:"scope_model,omitempty"`
	UserPath       string `json:"scope_user_path,omitempty"`
}

func (s Scope) MarshalJSON() ([]byte, error) {
	return json.Marshal(scopeJSON{
		ProviderName: strings.TrimSpace(s.Provider),
		Model:        strings.TrimSpace(s.Model),
		UserPath:     strings.TrimSpace(s.UserPath),
	})
}

func (s *Scope) UnmarshalJSON(data []byte) error {
	if s == nil {
		return nil
	}
	var raw scopeJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	providerName := strings.TrimSpace(raw.ProviderName)
	if providerName == "" {
		providerName = strings.TrimSpace(raw.LegacyProvider)
	}
	s.Provider = providerName
	s.Model = strings.TrimSpace(raw.Model)
	s.UserPath = strings.TrimSpace(raw.UserPath)
	return nil
}

// Payload is the immutable persisted workflow JSON document.
type Payload struct {
	SchemaVersion int          `json:"schema_version" bson:"schema_version"`
	Features      FeatureFlags `json:"features" bson:"features"`
	// Guardrails is the version 1 step list (prompt phase only).
	Guardrails []GuardrailStep `json:"guardrails,omitempty" bson:"guardrails,omitempty"`
	// Steps is the version 2 step list with a phase per step.
	Steps []Step `json:"steps,omitempty" bson:"steps,omitempty"`
}

// Step references one named guardrail instance in one phase at one step.
type Step struct {
	Ref   string `json:"ref" bson:"ref"`
	Phase string `json:"phase,omitempty" bson:"phase,omitempty"`
	Step  int    `json:"step" bson:"step"`
}

// EffectiveSteps returns the steps to compile regardless of schema version:
// version 1 guardrails become prompt steps.
func (p Payload) EffectiveSteps() []Step {
	if len(p.Steps) > 0 {
		return append([]Step(nil), p.Steps...)
	}
	steps := make([]Step, 0, len(p.Guardrails))
	for _, guardrail := range p.Guardrails {
		steps = append(steps, Step{Ref: guardrail.Ref, Phase: PhasePrompt, Step: guardrail.Step})
	}
	return steps
}

func phaseOrder(phase string) int {
	switch phase {
	case PhasePrompt:
		return 0
	case PhaseResponse:
		return 1
	case PhaseStream:
		return 2
	default:
		return 3
	}
}

func normalizePhase(phase string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "", PhasePrompt:
		return PhasePrompt, nil
	case PhaseResponse:
		return PhaseResponse, nil
	case PhaseStream:
		return PhaseStream, nil
	default:
		return "", newValidationError("invalid step phase: "+strings.TrimSpace(phase)+" (must be prompt, response, or stream)", nil)
	}
}

// FeatureFlags configures gateway-owned behaviors for a request.
type FeatureFlags struct {
	Cache      bool  `json:"cache" bson:"cache"`
	Audit      bool  `json:"audit" bson:"audit"`
	Usage      bool  `json:"usage" bson:"usage"`
	Budget     *bool `json:"budget,omitempty" bson:"budget,omitempty"`
	Guardrails bool  `json:"guardrails" bson:"guardrails"`
	Failover   *bool `json:"failover,omitempty" bson:"failover,omitempty"`
}

func (f FeatureFlags) canonicalize() FeatureFlags {
	if f.Budget == nil {
		budgetEnabled := true
		f.Budget = &budgetEnabled
	}
	if f.Failover == nil {
		failoverEnabled := true
		f.Failover = &failoverEnabled
	}
	return f
}

func (f FeatureFlags) runtimeFeatures() core.WorkflowFeatures {
	f = f.canonicalize()
	return core.WorkflowFeatures{
		Cache:      f.Cache,
		Audit:      f.Audit,
		Usage:      f.Usage,
		Budget:     f.Usage && *f.Budget,
		Guardrails: f.Guardrails,
		Failover:   *f.Failover,
	}
}

// GuardrailStep references one named guardrail and its execution step.
type GuardrailStep struct {
	Ref  string `json:"ref" bson:"ref"`
	Step int    `json:"step" bson:"step"`
}

// Version is one immutable persisted workflow version row.
type Version struct {
	ID           string    `json:"id" bson:"_id"`
	Scope        Scope     `json:"scope" bson:"-"`
	ScopeKey     string    `json:"scope_key" bson:"scope_key"`
	Version      int       `json:"version" bson:"version"`
	Active       bool      `json:"active" bson:"active"`
	Managed      bool      `json:"managed_default,omitempty" bson:"managed_default,omitempty"`
	Name         string    `json:"name" bson:"name"`
	Description  string    `json:"description,omitempty" bson:"description,omitempty"`
	Payload      Payload   `json:"workflow_payload" bson:"workflow_payload"`
	WorkflowHash string    `json:"workflow_hash" bson:"workflow_hash"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
}

// CreateInput is the authoring input for one new immutable workflow version.
type CreateInput struct {
	Scope       Scope
	Activate    bool
	Managed     bool
	Name        string
	Description string
	Payload     Payload
}

func normalizeScope(scope Scope) (Scope, string, error) {
	scope.Provider = strings.TrimSpace(scope.Provider)
	scope.Model = strings.TrimSpace(scope.Model)
	userPath, err := core.NormalizeUserPath(scope.UserPath)
	if err != nil {
		return Scope{}, "", newValidationError("invalid scope_user_path", err)
	}
	scope.UserPath = userPath
	if scope.Provider == "" && scope.Model != "" {
		return Scope{}, "", newValidationError("scope_model requires scope_provider_name", nil)
	}
	if strings.Contains(scope.Provider, ":") || strings.Contains(scope.Model, ":") || strings.Contains(scope.UserPath, ":") {
		return Scope{}, "", newValidationError("scope fields cannot contain ':'", nil)
	}
	return scope, scopeKey(scope), nil
}

func scopeKey(scope Scope) string {
	switch {
	case scope.Provider == "" && scope.UserPath == "":
		return "global"
	case scope.Provider == "" && scope.UserPath != "":
		return "path:" + scope.UserPath
	case scope.Model == "" && scope.UserPath == "":
		return "provider:" + scope.Provider
	case scope.Model == "" && scope.UserPath != "":
		return "provider_path:" + scope.Provider + ":" + scope.UserPath
	case scope.UserPath == "":
		return "provider_model:" + scope.Provider + ":" + scope.Model
	default:
		return "provider_model_path:" + scope.Provider + ":" + scope.Model + ":" + scope.UserPath
	}
}

func normalizePayload(payload Payload) (Payload, string, error) {
	if payload.SchemaVersion == 0 {
		payload.SchemaVersion = currentSchemaVersion
		if len(payload.Steps) == 0 && len(payload.Guardrails) > 0 {
			payload.SchemaVersion = 1
		}
	}
	payload.Features = payload.Features.canonicalize()
	switch payload.SchemaVersion {
	case 1:
		if len(payload.Steps) > 0 {
			return Payload{}, "", newValidationError("schema_version 1 payloads use guardrails, not steps", nil)
		}
		return normalizeLegacyPayload(payload)
	case 2:
		return normalizeStepsPayload(payload)
	default:
		return Payload{}, "", newValidationError("unsupported schema_version", nil)
	}
}

// normalizeStepsPayload validates a version 2 payload: legacy guardrails are
// folded into prompt steps, phases are validated, (ref, phase) pairs are
// unique, and steps are sorted by phase, step, ref.
func normalizeStepsPayload(payload Payload) (Payload, string, error) {
	type indexedStep struct {
		step  Step
		index int
	}
	steps := payload.Steps
	for _, guardrail := range payload.Guardrails {
		steps = append(steps, Step{Ref: guardrail.Ref, Phase: PhasePrompt, Step: guardrail.Step})
	}
	payload.Guardrails = nil

	indexed := make([]indexedStep, 0, len(steps))
	seen := make(map[string]struct{}, len(steps))
	for i, step := range steps {
		step.Ref = strings.TrimSpace(step.Ref)
		if step.Ref == "" {
			return Payload{}, "", newValidationError("guardrail ref is required", nil)
		}
		if step.Step < 0 {
			return Payload{}, "", newValidationError("guardrail step must not be negative: "+step.Ref, nil)
		}
		phase, err := normalizePhase(step.Phase)
		if err != nil {
			return Payload{}, "", err
		}
		step.Phase = phase
		key := phase + ":" + step.Ref
		if _, exists := seen[key]; exists {
			return Payload{}, "", newValidationError("duplicate guardrail ref in "+phase+" phase: "+step.Ref, nil)
		}
		seen[key] = struct{}{}
		indexed = append(indexed, indexedStep{step: step, index: i})
	}
	sort.SliceStable(indexed, func(i, j int) bool {
		left, right := indexed[i].step, indexed[j].step
		if phaseOrder(left.Phase) != phaseOrder(right.Phase) {
			return phaseOrder(left.Phase) < phaseOrder(right.Phase)
		}
		if left.Step != right.Step {
			return left.Step < right.Step
		}
		if left.Ref != right.Ref {
			return left.Ref < right.Ref
		}
		return indexed[i].index < indexed[j].index
	})
	payload.Steps = nil
	for _, item := range indexed {
		payload.Steps = append(payload.Steps, item.step)
	}
	return hashPayload(payload)
}

func hashPayload(payload Payload) (Payload, string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Payload{}, "", newValidationError("marshal workflow payload", err)
	}
	sum := sha256.Sum256(raw)
	return payload, hex.EncodeToString(sum[:]), nil
}

func normalizeLegacyPayload(payload Payload) (Payload, string, error) {
	type indexedGuardrail struct {
		step  GuardrailStep
		index int
	}

	indexed := make([]indexedGuardrail, 0, len(payload.Guardrails))
	seenRefs := make(map[string]struct{}, len(payload.Guardrails))
	for i, guardrail := range payload.Guardrails {
		guardrail.Ref = strings.TrimSpace(guardrail.Ref)
		if guardrail.Ref == "" {
			return Payload{}, "", newValidationError("guardrail ref is required", nil)
		}
		if guardrail.Step < 0 {
			return Payload{}, "", newValidationError("guardrail step must not be negative: "+guardrail.Ref, nil)
		}
		if _, exists := seenRefs[guardrail.Ref]; exists {
			return Payload{}, "", newValidationError("duplicate guardrail ref: "+guardrail.Ref, nil)
		}
		seenRefs[guardrail.Ref] = struct{}{}
		indexed = append(indexed, indexedGuardrail{step: guardrail, index: i})
	}

	sort.SliceStable(indexed, func(i, j int) bool {
		if indexed[i].step.Step != indexed[j].step.Step {
			return indexed[i].step.Step < indexed[j].step.Step
		}
		if indexed[i].step.Ref != indexed[j].step.Ref {
			return indexed[i].step.Ref < indexed[j].step.Ref
		}
		return indexed[i].index < indexed[j].index
	})

	payload.Guardrails = payload.Guardrails[:0]
	for _, item := range indexed {
		payload.Guardrails = append(payload.Guardrails, item.step)
	}
	return hashPayload(payload)
}

func normalizeCreateInput(input CreateInput) (CreateInput, string, string, error) {
	scope, scopeKey, err := normalizeScope(input.Scope)
	if err != nil {
		return CreateInput{}, "", "", err
	}

	payload, workflowHash, err := normalizePayload(input.Payload)
	if err != nil {
		return CreateInput{}, "", "", err
	}

	input.Scope = scope
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Payload = payload
	if input.Managed && (scope.Provider != "" || scope.Model != "" || scope.UserPath != "") {
		return CreateInput{}, "", "", newValidationError("managed default workflow must use global scope", nil)
	}
	if !input.Managed &&
		input.Name == ManagedDefaultGlobalName &&
		input.Description == ManagedDefaultGlobalDescription {
		return CreateInput{}, "", "", newValidationError("managed default workflow name/description is reserved", nil)
	}
	return input, scopeKey, workflowHash, nil
}
