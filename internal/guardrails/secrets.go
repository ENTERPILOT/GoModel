package guardrails

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// DefinitionSecretEntity is the SecretKey.Entity of guardrail definitions and
// the first segment of their secret field paths, for example
// "guardrail_definitions.pii.config.api_key" (ADR-0014 §4). It is not
// "guardrails" so it never collides with the paths of the guardrails section
// of config.yaml. The secret fields are the plugin's InputSecret fields.
const DefinitionSecretEntity = "guardrail_definitions"

func definitionSecretEntity(name string) string {
	return DefinitionSecretEntity + "." + name
}

func configSecretField(key string) string {
	return "config." + key
}

// resolveDefinition returns the instance spec of def with the secret
// references of its config resolved, and the resolution to record once the
// instance serves.
func (s *Service) resolveDefinition(ctx context.Context, schema []pluginapi.Field, def Definition) (plugins.InstanceSpec, *config.ResolvedEntity, error) {
	entity := definitionSecretEntity(def.Name)
	fields := make(map[string]string)
	for key, value := range plugins.SecretValues(schema, def.Config) {
		fields[entity+"."+configSecretField(key)] = value
	}
	resolved, err := s.secrets.ResolveEntity(ctx, entity, fields)
	if err != nil {
		return plugins.InstanceSpec{}, nil, err
	}
	spec := instanceSpec(def)
	spec.Config, err = plugins.MapSecrets(schema, def.Config, func(key, _ string) (string, error) {
		return resolved.Value(entity + "." + configSecretField(key)), nil
	})
	if err != nil {
		return plugins.InstanceSpec{}, nil, err
	}
	return spec, resolved, nil
}

// storeDefinitionSecrets writes every literal secret of def's config through
// the generation's SecretWriter, when one is registered, replacing it with
// the returned reference. It returns the references it created, which the
// caller releases if the save then fails. keep holds the values of the
// definition still stored: a writer-owned reference it does not hold is
// rejected, and if a write fails the references already created are
// released, except those in keep, which a writer that reuses a reference may
// have returned again.
func (s *Service) storeDefinitionSecrets(ctx context.Context, def *Definition, keep []string) ([]string, error) {
	entry, ok := s.catalog.Lookup(def.Type)
	if !ok {
		return nil, nil
	}
	var written []string
	stored, err := plugins.MapSecrets(entry.Manifest.ConfigSchema, def.Config, func(key, value string) (string, error) {
		reference, err := s.secrets.StoreSecret(ctx, config.SecretKey{Entity: DefinitionSecretEntity, ID: def.Name, Field: configSecretField(key)}, value, keep)
		if err == nil && reference != value {
			written = append(written, reference)
		}
		return reference, err
	})
	if err != nil {
		s.releaseSecrets(ctx, def.Name, written, keep)
		if errors.Is(err, config.ErrSecretNotHeld) {
			return nil, newValidationError(err.Error(), err)
		}
		return nil, err
	}
	def.Config = stored
	return written, nil
}

// definitionSecretValues lists the secret values of def's config.
func (s *Service) definitionSecretValues(def Definition) []string {
	entry, ok := s.catalog.Lookup(def.Type)
	if !ok {
		return nil
	}
	return slices.Collect(maps.Values(plugins.SecretValues(entry.Manifest.ConfigSchema, def.Config)))
}

// storedSecretValues lists the secret values of the cached definition name.
func (s *Service) storedSecretValues(name string) []string {
	s.mu.RLock()
	def, ok := s.snapshot.definitions[name]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.definitionSecretValues(def)
}

// releaseSecrets deletes the writer-owned references of previous that current
// no longer holds. It is best effort: the change it follows is committed.
func (s *Service) releaseSecrets(ctx context.Context, name string, previous, current []string) {
	if err := s.secrets.ReleaseSecrets(ctx, previous, current); err != nil {
		slog.Warn("failed to delete secrets of a guardrail from the secret store", "guardrail", name, "error", err)
	}
}

// RotateSecrets rebuilds the guardrail instances owning fields, as reported
// by a secret recheck, with their references resolved again (ADR-0014 §5).
// Every other instance is kept. When a reference cannot be re-resolved, or an
// instance no longer builds, every instance keeps running as it is.
//
// Compiled workflows pick the new instances up on their next refresh; the
// replaced ones stay open until then.
func (s *Service) RotateSecrets(ctx context.Context, fields []string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	rotated := func(name string) bool {
		prefix := definitionSecretEntity(name) + "."
		return slices.ContainsFunc(fields, func(field string) bool { return strings.HasPrefix(field, prefix) })
	}
	definitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	next, err := s.buildSnapshot(ctx, definitions, rotated)
	if err != nil {
		return guardrailServiceError("rebuild guardrails for rotated secrets", err)
	}
	s.swap(ctx, next)
	s.probeHealth(ctx, next)
	return nil
}
