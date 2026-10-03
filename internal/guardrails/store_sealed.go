package guardrails

import (
	"context"

	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/plugins"
)

// guardrailSecretKind names guardrail definitions in sealed values' AAD.
const guardrailSecretKind = "guardrail"

// secretKeysFunc returns the secret config keys of a plugin type, from its
// manifest schema.
type secretKeysFunc func(pluginType string) map[string]bool

func catalogSecretKeys(catalog *plugins.Catalog) secretKeysFunc {
	return func(pluginType string) map[string]bool {
		if catalog == nil {
			return nil
		}
		entry, ok := catalog.Lookup(normalizeDefinitionType(pluginType))
		if !ok {
			return nil
		}
		return plugins.SecretKeys(entry.Manifest.ConfigSchema)
	}
}

// sealedStore encrypts a definition's secret config values (the fields its
// plugin schema marks pluginapi.InputSecret) on the way into the store and
// decrypts them on the way out.
//
// Reads open every sealed value whether or not the plugin is still in the
// catalog, so a definition never depends on its schema to be readable.
type sealedStore struct {
	Store
	box        *encryption.Box
	secretKeys secretKeysFunc
}

func sealStore(store Store, box *encryption.Box, secretKeys secretKeysFunc) Store {
	if box == nil {
		return store
	}
	return &sealedStore{Store: store, box: box, secretKeys: secretKeys}
}

func (s *sealedStore) open(definition *Definition) error {
	secret := s.secretKeys(definition.Type)
	id := normalizeDefinitionName(definition.Name)
	config, err := plugins.MapConfigStrings(definition.Config, func(key, value string) (string, error) {
		if !secret[key] && !encryption.IsSealed(value) {
			return value, nil
		}
		return openField(s.box, id, key, value)
	})
	if err != nil {
		return err
	}
	definition.Config = config
	return nil
}

func (s *sealedStore) seal(definition *Definition) error {
	secret := s.secretKeys(definition.Type)
	if len(secret) == 0 {
		return nil
	}
	id := normalizeDefinitionName(definition.Name)
	config, err := plugins.MapConfigStrings(definition.Config, func(key, value string) (string, error) {
		if !secret[key] {
			return value, nil
		}
		field := encryption.Field{Name: "config." + key, Value: &value}
		err := s.box.SealFields(guardrailSecretKind, id, field)
		return value, err
	})
	if err != nil {
		return err
	}
	definition.Config = config
	return nil
}

func openField(box *encryption.Box, id, key, value string) (string, error) {
	field := encryption.Field{Name: "config." + key, Value: &value}
	err := box.OpenFields(guardrailSecretKind, id, field)
	return value, err
}

func (s *sealedStore) List(ctx context.Context) ([]Definition, error) {
	definitions, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range definitions {
		if err := s.open(&definitions[i]); err != nil {
			return nil, err
		}
	}
	return definitions, nil
}

func (s *sealedStore) Get(ctx context.Context, name string) (*Definition, error) {
	definition, err := s.Store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.open(definition); err != nil {
		return nil, err
	}
	return definition, nil
}

func (s *sealedStore) Upsert(ctx context.Context, definition Definition) error {
	if err := s.seal(&definition); err != nil {
		return err
	}
	return s.Store.Upsert(ctx, definition)
}

func (s *sealedStore) UpsertMany(ctx context.Context, definitions []Definition) error {
	sealed := make([]Definition, len(definitions))
	for i, definition := range definitions {
		if err := s.seal(&definition); err != nil {
			return err
		}
		sealed[i] = definition
	}
	return s.Store.UpsertMany(ctx, sealed)
}

// needsReseal reports whether a stored definition holds a plaintext secret or
// a value sealed with an older data key.
func (s *sealedStore) needsReseal(definition Definition) (bool, error) {
	secret := s.secretKeys(definition.Type)
	var fields []encryption.Field
	_, err := plugins.MapConfigStrings(definition.Config, func(key, value string) (string, error) {
		if secret[key] || encryption.IsSealed(value) {
			fields = append(fields, encryption.Field{Name: key, Value: &value})
		}
		return value, nil
	})
	return s.box.NeedsReseal(fields...), err
}

// reencrypt rewrites every definition holding a plaintext secret or a value
// sealed with an older data key.
func (s *sealedStore) reencrypt(ctx context.Context) (encryption.Report, error) {
	report := encryption.Report{Entity: "guardrail_definitions"}
	raw, err := s.Store.List(ctx)
	if err != nil {
		return report, err
	}
	report.Rows = len(raw)
	for i := range raw {
		definition := raw[i]
		stale, err := s.needsReseal(definition)
		if err != nil {
			return report, err
		}
		if !stale {
			continue
		}
		if err := s.open(&definition); err != nil {
			return report, err
		}
		if err := s.Upsert(ctx, definition); err != nil {
			return report, err
		}
		report.Reencrypted++
	}
	return report, nil
}
