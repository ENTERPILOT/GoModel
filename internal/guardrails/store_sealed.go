package guardrails

import (
	"context"
	"errors"

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
	swap       configSwapper // nil when the store cannot swap; see confirmKey
}

func sealStore(store Store, box *encryption.Box, secretKeys secretKeysFunc) Store {
	if box == nil {
		return store
	}
	swap, _ := store.(configSwapper)
	return &sealedStore{Store: store, box: box, secretKeys: secretKeys, swap: swap}
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
	return s.sealKeys(definition, s.secretKeys(definition.Type))
}

// sealKeys seals the config values under the given keys.
func (s *sealedStore) sealKeys(definition *Definition, secret map[string]bool) error {
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
	sealed := definition
	if err := s.seal(&sealed); err != nil {
		return err
	}
	if err := s.Store.Upsert(ctx, sealed); err != nil {
		return err
	}
	return s.confirmKey(ctx, definition, sealed)
}

func (s *sealedStore) UpsertMany(ctx context.Context, definitions []Definition) error {
	sealed := make([]Definition, len(definitions))
	for i, definition := range definitions {
		if err := s.seal(&definition); err != nil {
			return err
		}
		sealed[i] = definition
	}
	if err := s.Store.UpsertMany(ctx, sealed); err != nil {
		return err
	}
	for i := range definitions {
		if err := s.confirmKey(ctx, definitions[i], sealed[i]); err != nil {
			return err
		}
	}
	return nil
}

// confirmKey re-seals a just-written row when a data key rotation activated a
// new key while the save was in flight (see Box.RotatedSinceSeal). The swap is
// conditional, so a newer save of the same row is never overwritten.
func (s *sealedStore) confirmKey(ctx context.Context, plain, written Definition) error {
	if s.swap == nil {
		return nil
	}
	_, fields, err := s.storedSecretKeys(written)
	if err != nil || !s.box.RotatedSinceSeal(fields...) {
		return err
	}
	resealed := plain
	if err := s.seal(&resealed); err != nil {
		return err
	}
	_, err = s.swap.swapConfig(ctx, written, resealed)
	return err
}

// storedSecretKeys returns the keys of a stored definition that hold a secret:
// those its plugin schema marks secret, plus any already sealed. The second
// set matters when the plugin is missing from the catalog or no longer marks
// the field secret, so re-encryption never writes a sealed value back as
// plaintext.
func (s *sealedStore) storedSecretKeys(definition Definition) (map[string]bool, []encryption.Field, error) {
	keys := map[string]bool{}
	for key := range s.secretKeys(definition.Type) {
		keys[key] = true
	}
	var fields []encryption.Field
	_, err := plugins.MapConfigStrings(definition.Config, func(key, value string) (string, error) {
		if encryption.IsSealed(value) {
			keys[key] = true
		}
		if keys[key] {
			fields = append(fields, encryption.Field{Name: key, Value: &value})
		}
		return value, nil
	})
	return keys, fields, err
}

// configSwapper replaces a definition's config only while it still holds
// the values read earlier. Both store backends implement it.
type configSwapper interface {
	swapConfig(ctx context.Context, current, next Definition) (bool, error)
}

// reencrypt rewrites every definition holding a plaintext secret or a value
// sealed with an older data key. Each write is conditional on the config
// still being what was read, so a concurrent admin edit is never overwritten:
// the row is read again and retried.
func (s *sealedStore) reencrypt(ctx context.Context, swap configSwapper) (encryption.Report, error) {
	listed, err := s.Store.List(ctx)
	if err != nil {
		return encryption.Report{Entity: "guardrail_definitions"}, err
	}
	names := make([]string, len(listed))
	for i, row := range listed {
		names[i] = row.Name
	}
	return encryption.ReencryptRows("guardrail_definitions", names, func(name string) (encryption.RowOutcome, error) {
		return s.reencryptRow(ctx, swap, name)
	})
}

func (s *sealedStore) reencryptRow(ctx context.Context, swap configSwapper, name string) (encryption.RowOutcome, error) {
	current, err := s.Store.Get(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return encryption.RowUnchanged, nil
	}
	if err != nil {
		return 0, err
	}
	keys, fields, err := s.storedSecretKeys(*current)
	if err != nil {
		return 0, err
	}
	if !s.box.NeedsReseal(fields...) {
		return encryption.RowUnchanged, nil
	}
	next := *current
	if err := s.open(&next); err != nil {
		return 0, err
	}
	if err := s.sealKeys(&next, keys); err != nil {
		return 0, err
	}
	swapped, err := swap.swapConfig(ctx, *current, next)
	if err != nil {
		return 0, err
	}
	if !swapped {
		return encryption.RowConflict, nil
	}
	return encryption.RowRewritten, nil
}
