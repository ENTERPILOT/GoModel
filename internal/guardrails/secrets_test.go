package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// guardrailVault resolves ${vault:...} from a mutable map.
type guardrailVault struct {
	mu     sync.Mutex
	values map[string]string
	fields []string
}

func (v *guardrailVault) set(reference, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[reference] = value
}

func (v *guardrailVault) ResolveSecret(ctx context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	field, _ := config.SecretFieldFromContext(ctx)
	v.fields = append(v.fields, field)
	value, ok := v.values[reference]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

type guardrailWriter struct {
	vault   *guardrailVault
	deleted []string
}

func (w *guardrailWriter) WriteSecret(_ context.Context, key config.SecretKey, value string) (string, error) {
	reference := "written/" + key.ID + "/" + key.Field
	w.vault.set(reference, value)
	return "${vault:" + reference + "}", nil
}

func (w *guardrailWriter) DeleteSecret(_ context.Context, reference string) error {
	w.deleted = append(w.deleted, reference)
	return nil
}

func (w *guardrailWriter) OwnsReference(reference string) bool {
	return strings.HasPrefix(reference, "${vault:written/")
}

func secretDefinition(name, apiKey string) Definition {
	return Definition{Name: name, Type: "secret_check", Config: json.RawMessage(`{"api_key":"` + apiKey + `"}`)}
}

func newSecretsService(t *testing.T, store Store, vault *guardrailVault) (*Service, *config.Secrets) {
	t.Helper()
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	service, err := NewService(store, testCatalog(t), plugins.HostDeps{})
	require.NoError(t, err)
	service.secrets = secrets
	require.NoError(t, service.Refresh(t.Context()))
	return service, secrets
}

// instanceAPIKey returns the api_key the named instance was built with.
func instanceAPIKey(t *testing.T, service *Service, name string) string {
	t.Helper()
	service.mu.RLock()
	inst := service.snapshot.instances[name]
	service.mu.RUnlock()
	require.NotNil(t, inst)
	plugin, ok := inst.Plugin.(*secretPlugin)
	require.True(t, ok)
	var cfg struct {
		APIKey string `json:"api_key"`
	}
	require.NoError(t, json.Unmarshal(plugin.config, &cfg))
	return cfg.APIKey
}

func TestServiceResolvesSecretReferencesWhenBuilding(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{"pii": "resolved-key"}}
	store := newTestStore(secretDefinition("pii", "${vault:pii}"))
	service, _ := newSecretsService(t, store, vault)

	assert.Equal(t, "resolved-key", instanceAPIKey(t, service, "pii"))
	assert.Contains(t, vault.fields, "guardrail_definitions.pii.config.api_key")

	view, ok := service.Get("pii")
	require.True(t, ok)
	assert.JSONEq(t, `{"api_key":"${vault:pii}","threshold":0.5}`, string(view.Config), "a reference is not masked")

	resolved, _, ok := service.InstanceConfig("pii")
	require.True(t, ok)
	assert.Contains(t, string(resolved), "resolved-key")
}

func TestServiceUpsertRejectsUnresolvableReference(t *testing.T) {
	store := newTestStore()
	service, _ := newSecretsService(t, store, &guardrailVault{values: map[string]string{}})

	err := service.Upsert(t.Context(), secretDefinition("pii", "${vault:missing}"))
	require.Error(t, err)
	assert.True(t, IsValidationError(err), "%v", err)
	assert.Contains(t, err.Error(), "guardrail_definitions.pii.config.api_key")
	assert.Empty(t, store.definitions)
}

func TestServiceRotateSecretsRebuildsOnlyTheAffectedInstance(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{"a": "a1", "b": "b1"}}
	store := newTestStore(secretDefinition("first", "${vault:a}"), secretDefinition("second", "${vault:b}"))
	service, secrets := newSecretsService(t, store, vault)
	ctx := t.Context()
	service.mu.RLock()
	secondBefore := service.snapshot.instances["second"]
	service.mu.RUnlock()

	vault.set("a", "a2")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"guardrail_definitions.first.config.api_key"}, recheck.Fields())
	require.NoError(t, service.RotateSecrets(ctx, recheck.Fields()))

	assert.Equal(t, "a2", instanceAPIKey(t, service, "first"))
	service.mu.RLock()
	assert.Same(t, secondBefore, service.snapshot.instances["second"])
	service.mu.RUnlock()

	recheck, err = secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields())

	delete(vault.values, "a")
	require.Error(t, service.RotateSecrets(ctx, []string{"guardrail_definitions.first.config.api_key"}))
	assert.Equal(t, "a2", instanceAPIKey(t, service, "first"), "a failed lookup keeps the instance")
}

func TestServiceRotateSecretsAppliesHealthyRotationsWhenOneFails(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{"a": "a1", "b": "b1"}}
	store := newTestStore(secretDefinition("first", "${vault:a}"), secretDefinition("second", "${vault:b}"))
	service, secrets := newSecretsService(t, store, vault)
	ctx := t.Context()

	vault.set("a", "a2")
	delete(vault.values, "b")
	err := service.RotateSecrets(ctx, []string{
		"guardrail_definitions.first.config.api_key",
		"guardrail_definitions.second.config.api_key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"second"`)
	assert.NotContains(t, err.Error(), `"first"`)
	assert.Equal(t, "a2", instanceAPIKey(t, service, "first"), "a healthy rotation applies")
	assert.Equal(t, "b1", instanceAPIKey(t, service, "second"), "the failed guardrail keeps its values")

	// The failed guardrail's change stays pending; the applied one is done.
	vault.set("b", "b2")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"guardrail_definitions.second.config.api_key"}, recheck.Fields())
	require.NoError(t, service.RotateSecrets(ctx, recheck.Fields()))
	assert.Equal(t, "b2", instanceAPIKey(t, service, "second"))
}

func TestServiceRotateSecretsMatchesDottedNamesExactly(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{"pii": "p1", "team": "t1"}}
	store := newTestStore(secretDefinition("pii", "${vault:pii}"), secretDefinition("pii.team", "${vault:team}"))
	service, secrets := newSecretsService(t, store, vault)
	ctx := t.Context()
	service.mu.RLock()
	piiBefore := service.snapshot.instances["pii"]
	service.mu.RUnlock()

	vault.set("team", "t2")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"guardrail_definitions.pii.team.config.api_key"}, recheck.Fields())
	require.NoError(t, service.RotateSecrets(ctx, recheck.Fields()))

	assert.Equal(t, "t2", instanceAPIKey(t, service, "pii.team"))
	service.mu.RLock()
	assert.Same(t, piiBefore, service.snapshot.instances["pii"], "pii does not own the fields of pii.team")
	service.mu.RUnlock()
}

func TestServiceSecretWriter(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{}}
	store := newTestStore()
	service, secrets := newSecretsService(t, store, vault)
	writer := &guardrailWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()

	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", "typed-key")))
	assert.JSONEq(t, `{"api_key":"${vault:written/pii/config.api_key}","threshold":0.5}`, string(store.definitions["pii"].Config))
	assert.Equal(t, "typed-key", instanceAPIKey(t, service, "pii"))

	// Sending the mask back keeps the written reference and deletes nothing.
	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", plugins.SecretMask)))
	assert.Empty(t, writer.deleted)

	require.NoError(t, service.Delete(ctx, "pii"))
	assert.Equal(t, []string{"${vault:written/pii/config.api_key}"}, writer.deleted)

	vault.set("written/pii/config.api_key", "rotated")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields(), "a deleted guardrail is no longer watched")
}

// A writer-owned reference is only saved to the guardrail whose stored
// definition holds it.
func TestServiceUpsertRejectsWriterReferencesTheDefinitionDoesNotHold(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{}}
	store := newTestStore()
	service, secrets := newSecretsService(t, store, vault)
	writer := &guardrailWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	owned := "${vault:written/pii/config.api_key}"

	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", "typed-key")))
	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", owned)), "the stored definition holds it")

	err := service.Upsert(ctx, secretDefinition("copy", owned))
	require.ErrorIs(t, err, config.ErrSecretNotHeld)
	assert.True(t, IsValidationError(err), "%v", err)
	assert.NotContains(t, store.definitions, "copy")

	require.NoError(t, service.Delete(ctx, "pii"))
	writer.deleted = nil
	require.ErrorIs(t, service.Upsert(ctx, secretDefinition("pii", owned)), config.ErrSecretNotHeld)
	assert.NotContains(t, store.definitions, "pii")
	assert.Empty(t, writer.deleted)
}

// The secret fields are found whatever spelling of the type reaches the
// writer, and a type the catalog does not know fails rather than letting a
// literal secret bypass it.
func TestServiceStoresSecretsOfUnnormalizedTypes(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{}}
	store := newTestStore()
	service, secrets := newSecretsService(t, store, vault)
	secrets.SetWriter(&guardrailWriter{vault: vault})
	ctx := t.Context()
	written := `{"api_key":"${vault:written/pii/config.api_key}","threshold":0.5}`

	definition := secretDefinition("pii", "typed-key")
	definition.Type = " Plugin:Secret_Check "
	require.NoError(t, service.Upsert(ctx, definition))
	assert.Equal(t, "secret_check", store.definitions["pii"].Type)
	assert.JSONEq(t, written, string(store.definitions["pii"].Config))

	for _, defType := range []string{"Secret_Check", "plugin:secret_check", " secret_check "} {
		definition := secretDefinition("raw", "typed-key")
		definition.Type = defType
		refs, err := service.storeDefinitionSecrets(ctx, &definition, nil)
		require.NoError(t, err, defType)
		assert.Equal(t, []string{"${vault:written/raw/config.api_key}"}, refs, defType)
		assert.Equal(t, []string{"${vault:written/raw/config.api_key}"}, service.definitionSecretValues(definition), defType)
	}

	unknown := Definition{Name: "x", Type: "no_such_plugin", Config: json.RawMessage(`{"api_key":"typed-key"}`)}
	_, err := service.storeDefinitionSecrets(ctx, &unknown, nil)
	require.Error(t, err)
	assert.True(t, IsValidationError(err))
	assert.JSONEq(t, `{"api_key":"typed-key"}`, string(unknown.Config), "nothing is stored for an unknown type")
}

// Saves and deletes clean up after the stored definition, not the cached
// snapshot, which another writer of the store can leave behind.
func TestServiceReleasesSecretsOfTheStoredDefinition(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{"written/pii/newer": "newer-key"}}
	store := newTestStore()
	service, secrets := newSecretsService(t, store, vault)
	writer := &guardrailWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	newer := "${vault:written/pii/newer}"

	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", "typed-key")))
	storeNewer := func() {
		definition := store.definitions["pii"]
		definition.Config = json.RawMessage(`{"api_key":"` + newer + `","threshold":0.5}`)
		store.definitions["pii"] = definition
	}

	// The mask keeps the stored reference, which the stored row holds.
	storeNewer()
	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", plugins.SecretMask)))
	assert.JSONEq(t, `{"api_key":"`+newer+`","threshold":0.5}`, string(store.definitions["pii"].Config))
	assert.Empty(t, writer.deleted)

	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", "typed-key")))
	assert.Equal(t, []string{newer}, writer.deleted, "the replaced stored reference is released")

	storeNewer()
	writer.deleted = nil
	require.NoError(t, service.Delete(ctx, "pii"))
	assert.Equal(t, []string{newer}, writer.deleted)
}

// Configuration seeding persists the definition as given: a secret reference
// reaches the database unresolved, and only the built instance sees the value.
func TestServiceSeedStoresReferencesNotValues(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := t.Context()
		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		vault := &guardrailVault{values: map[string]string{"pii": "resolved-key"}}
		service, _ := newSecretsService(t, store, vault)

		require.NoError(t, service.UpsertDefinitions(ctx, []Definition{secretDefinition("pii", "${vault:pii}")}))

		row, err := store.Get(ctx, "pii")
		require.NoError(t, err)
		assert.Contains(t, string(row.Config), "${vault:pii}")
		assert.NotContains(t, string(row.Config), "resolved-key")
		assert.Equal(t, "resolved-key", instanceAPIKey(t, service, "pii"))
	})
}

// sealUnconfirmedStore reports every save, once unconfirmed is set, as
// written with an unconfirmed data key.
type sealUnconfirmedStore struct {
	*testStore
	unconfirmed bool
}

func (s *sealUnconfirmedStore) Upsert(ctx context.Context, definition Definition) error {
	if err := s.testStore.Upsert(ctx, definition); err != nil {
		return err
	}
	if s.unconfirmed {
		return encryption.ErrSealUnconfirmed
	}
	return nil
}

// A save whose data key is unconfirmed was written: it is applied, the
// secrets it replaced are released, and the ones it wrote are kept.
func TestServiceUnconfirmedSaveKeepsWhatItWrote(t *testing.T) {
	vault := &guardrailVault{values: map[string]string{"hand": "hand-key"}}
	store := &sealUnconfirmedStore{testStore: newTestStore()}
	service, secrets := newSecretsService(t, store, vault)
	writer := &guardrailWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	written := "${vault:written/pii/config.api_key}"

	require.NoError(t, service.Upsert(ctx, secretDefinition("pii", "typed-key")))
	store.unconfirmed = true

	err := service.Upsert(ctx, secretDefinition("pii", "${vault:hand}"))
	require.ErrorIs(t, err, encryption.ErrSealUnconfirmed)
	assert.Equal(t, []string{written}, writer.deleted, "the replaced secret is released")
	assert.Equal(t, "hand-key", instanceAPIKey(t, service, "pii"), "the saved definition is applied")

	writer.deleted = nil
	err = service.Upsert(ctx, secretDefinition("pii", "again"))
	require.ErrorIs(t, err, encryption.ErrSealUnconfirmed)
	assert.Empty(t, writer.deleted, "the secret the stored definition now holds is kept")
	assert.JSONEq(t, `{"api_key":"`+written+`","threshold":0.5}`, string(store.definitions["pii"].Config))
	assert.Equal(t, "again", instanceAPIKey(t, service, "pii"))
}
