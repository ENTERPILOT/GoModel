package executionplans

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"gomodel/internal/core"
)

const currentSchemaVersion = 1

// Scope identifies the request selector a persisted execution plan applies to.
type Scope struct {
	Provider string `json:"scope_provider,omitempty" bson:"scope_provider,omitempty"`
	Model    string `json:"scope_model,omitempty" bson:"scope_model,omitempty"`
	UserPath string `json:"scope_user_path,omitempty" bson:"scope_user_path,omitempty"`
}

// Payload is the immutable persisted execution-plan JSON document.
type Payload struct {
	SchemaVersion int             `json:"schema_version" bson:"schema_version"`
	Features      FeatureFlags    `json:"features" bson:"features"`
	Guardrails    []GuardrailStep `json:"guardrails,omitempty" bson:"guardrails,omitempty"`
}

// FeatureFlags configures gateway-owned behaviors for a request.
type FeatureFlags struct {
	Cache      bool  `json:"cache" bson:"cache"`
	Audit      bool  `json:"audit" bson:"audit"`
	Usage      bool  `json:"usage" bson:"usage"`
	Guardrails bool  `json:"guardrails" bson:"guardrails"`
	Fallback   *bool `json:"fallback,omitempty" bson:"fallback,omitempty"`
}

func (f FeatureFlags) canonicalize() FeatureFlags {
	if f.Fallback != nil {
		return f
	}
	fallbackEnabled := true
	f.Fallback = &fallbackEnabled
	return f
}

func (f FeatureFlags) runtimeFeatures() core.ExecutionFeatures {
	f = f.canonicalize()
	fallback := true
	if f.Fallback != nil {
		fallback = *f.Fallback
	}
	return core.ExecutionFeatures{
		Cache:      f.Cache,
		Audit:      f.Audit,
		Usage:      f.Usage,
		Guardrails: f.Guardrails,
		Fallback:   fallback,
	}
}

// GuardrailStep references one named guardrail and its execution step.
type GuardrailStep struct {
	Ref  string `json:"ref" bson:"ref"`
	Step int    `json:"step" bson:"step"`
}

// Version is one immutable persisted execution-plan version row.
type Version struct {
	ID          string    `json:"id" bson:"_id"`
	Scope       Scope     `json:"scope" bson:"-"`
	ScopeKey    string    `json:"scope_key" bson:"scope_key"`
	Version     int       `json:"version" bson:"version"`
	Active      bool      `json:"active" bson:"active"`
	Name        string    `json:"name" bson:"name"`
	Description string    `json:"description,omitempty" bson:"description,omitempty"`
	Payload     Payload   `json:"plan_payload" bson:"plan_payload"`
	PlanHash    string    `json:"plan_hash" bson:"plan_hash"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
}

// CreateInput is the authoring input for one new immutable execution-plan version.
type CreateInput struct {
	Scope       Scope
	Activate    bool
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
		return Scope{}, "", newValidationError("scope_model requires scope_provider", nil)
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
	}
	if payload.SchemaVersion != currentSchemaVersion {
		return Payload{}, "", newValidationError("unsupported schema_version", nil)
	}
	payload.Features = payload.Features.canonicalize()

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

	raw, err := json.Marshal(payload)
	if err != nil {
		return Payload{}, "", newValidationError("marshal plan payload", err)
	}
	sum := sha256.Sum256(raw)
	return payload, hex.EncodeToString(sum[:]), nil
}

func normalizeCreateInput(input CreateInput) (CreateInput, string, string, error) {
	scope, scopeKey, err := normalizeScope(input.Scope)
	if err != nil {
		return CreateInput{}, "", "", err
	}

	payload, planHash, err := normalizePayload(input.Payload)
	if err != nil {
		return CreateInput{}, "", "", err
	}

	input.Scope = scope
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Payload = payload
	return input, scopeKey, planHash, nil
}
