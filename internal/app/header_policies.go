package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/headerpolicy"
)

// migrateLegacyHeaderPolicies moves preview-era header_modification rows into
// the dedicated header-policy store. Existing dedicated rows win by name, so
// the migration is idempotent after a partial shutdown or multi-instance race.
func migrateLegacyHeaderPolicies(ctx context.Context, legacy guardrails.Store, target headerpolicy.Store) (int, error) {
	definitions, err := legacy.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list legacy header policies: %w", err)
	}
	toMigrate := make([]headerpolicy.Definition, 0)
	legacyNames := make([]string, 0)
	for _, definition := range definitions {
		if normalizeLegacyHeaderPolicyType(definition.Type) != "header_modification" {
			continue
		}
		legacyNames = append(legacyNames, definition.Name)
		if _, err := target.Get(ctx, definition.Name); err == nil {
			continue
		} else if !errors.Is(err, headerpolicy.ErrNotFound) {
			return 0, fmt.Errorf("check migrated header policy %q: %w", definition.Name, err)
		}
		policy, err := headerpolicy.DefinitionFromLegacy(definition.Name, definition.Description, definition.Config)
		if err != nil {
			return 0, fmt.Errorf("convert legacy header policy %q: %w", definition.Name, err)
		}
		policy.CreatedAt = definition.CreatedAt
		policy.UpdatedAt = definition.UpdatedAt
		toMigrate = append(toMigrate, policy)
	}
	if err := target.UpsertMany(ctx, toMigrate); err != nil {
		return 0, fmt.Errorf("persist migrated header policies: %w", err)
	}
	for _, name := range legacyNames {
		if err := legacy.Delete(ctx, name); err != nil && !errors.Is(err, guardrails.ErrNotFound) {
			return len(toMigrate), fmt.Errorf("remove migrated legacy header policy %q: %w", name, err)
		}
	}
	return len(toMigrate), nil
}

func configHeaderPolicyDefinitions(cfg *config.Config) ([]headerpolicy.Definition, map[string]int, error) {
	if cfg == nil {
		return nil, nil, nil
	}
	definitions := make([]headerpolicy.Definition, 0, len(cfg.HeaderPolicies.Policies))
	steps := make(map[string]int, len(cfg.HeaderPolicies.Policies))
	seen := make(map[string]string)
	appendPolicy := func(def headerpolicy.Definition, step int, source string) error {
		name := strings.TrimSpace(def.Name)
		if previous, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate header policy %q in %s and %s", name, previous, source)
		}
		seen[name] = source
		definitions = append(definitions, def)
		steps[name] = step
		return nil
	}
	for i, policy := range cfg.HeaderPolicies.Policies {
		actions := make([]headerpolicy.Action, 0, len(policy.Actions))
		for _, action := range policy.Actions {
			actions = append(actions, headerpolicy.Action{Action: action.Action, Header: action.Header, Value: action.Value, FromHeader: action.FromHeader})
		}
		conditions := make([]headerpolicy.Condition, 0, len(policy.When))
		for _, condition := range policy.When {
			conditions = append(conditions, headerpolicy.Condition{Header: condition.Header, Equals: condition.Equals, Matches: condition.Matches, Present: condition.Present})
		}
		if err := appendPolicy(headerpolicy.Definition{
			Name: policy.Name, Description: policy.Description, Methods: policy.Methods,
			Paths: policy.Paths, When: conditions, Actions: actions,
		}, policy.Step, fmt.Sprintf("header_policies.policies[%d]", i)); err != nil {
			return nil, nil, err
		}
	}
	for i, rule := range cfg.Guardrails.Rules {
		if normalizeLegacyHeaderPolicyType(rule.Type) != "header_modification" {
			continue
		}
		raw, err := json.Marshal(headerModificationSeedConfig(rule.HeaderModification))
		if err != nil {
			return nil, nil, fmt.Errorf("marshal legacy header policy %q: %w", rule.Name, err)
		}
		def, err := headerpolicy.DefinitionFromLegacy(rule.Name, "", raw)
		if err != nil {
			return nil, nil, err
		}
		if err := appendPolicy(def, rule.Order, fmt.Sprintf("guardrails.rules[%d]", i)); err != nil {
			return nil, nil, err
		}
		slog.Warn("guardrails header_modification config is deprecated; move it to header_policies.policies", "name", def.Name)
	}
	return definitions, steps, nil
}

func normalizeLegacyHeaderPolicyType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "header-modification":
		return "header_modification"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func rejectPolicyNameCollisions(guardrailDefinitions []guardrails.Definition, headerPolicyDefinitions []headerpolicy.Definition) error {
	headerPolicyNames := make(map[string]struct{}, len(headerPolicyDefinitions))
	for _, definition := range headerPolicyDefinitions {
		headerPolicyNames[strings.TrimSpace(definition.Name)] = struct{}{}
	}
	for _, definition := range guardrailDefinitions {
		name := strings.TrimSpace(definition.Name)
		if _, exists := headerPolicyNames[name]; exists {
			return fmt.Errorf("name %q is used by both a guardrail and a header policy", name)
		}
	}
	return nil
}
