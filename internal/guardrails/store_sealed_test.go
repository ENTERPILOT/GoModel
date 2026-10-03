package guardrails

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
)

// testSecretKeys marks api_key secret for the "presidio" plugin type only.
func testSecretKeys(pluginType string) map[string]bool {
	if pluginType == "presidio" {
		return map[string]bool{"api_key": true}
	}
	return nil
}

func storedConfigValues(t *testing.T, store Store, name string) map[string]any {
	t.Helper()
	definition, err := store.Get(context.Background(), name)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, json.Unmarshal(definition.Config, &values))
	return values
}

func TestSealedStoreEncryptsSecretConfigValues(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := sealStore(raw, box, testSecretKeys)

		config := `{"api_key":"pk-secret","url":"https://presidio.internal","threshold":0.5}`
		require.NoError(t, store.Upsert(ctx, Definition{Name: "pii", Type: "presidio", Config: []byte(config)}))
		require.NoError(t, store.UpsertMany(ctx, []Definition{
			{Name: "pii-2", Type: "presidio", Config: []byte(`{"api_key":"pk-two"}`)},
			{Name: "prompt", Type: "system_prompt", Config: []byte(`{"content":"be nice"}`)},
		}))

		values := storedConfigValues(t, raw, "pii")
		assert.True(t, encryption.IsSealed(values["api_key"].(string)))
		assert.Equal(t, "https://presidio.internal", values["url"], "non-secret values stay readable")
		assert.True(t, encryption.IsSealed(storedConfigValues(t, raw, "pii-2")["api_key"].(string)))
		assert.Equal(t, "be nice", storedConfigValues(t, raw, "prompt")["content"])

		got, err := store.Get(ctx, "pii")
		require.NoError(t, err)
		assertJSONEqual(t, got.Config, config)

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 3)
		assertJSONEqual(t, list[1].Config, `{"api_key":"pk-two"}`)

		// Sealed values open even when the plugin left the catalog.
		blind := sealStore(raw, box, func(string) map[string]bool { return nil })
		got, err = blind.Get(ctx, "pii")
		require.NoError(t, err)
		assertJSONEqual(t, got.Config, config)
	})
}

func TestSealedStoreRejectsMovedSecret(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := sealStore(raw, box, testSecretKeys)
		require.NoError(t, store.Upsert(ctx, Definition{Name: "pii", Type: "presidio", Config: []byte(`{"api_key":"pk-secret"}`)}))

		sealed := storedConfigValues(t, raw, "pii")["api_key"].(string)
		moved, err := json.Marshal(map[string]string{"api_key": sealed})
		require.NoError(t, err)
		require.NoError(t, raw.Upsert(ctx, Definition{Name: "other", Type: "presidio", Config: moved}))

		_, err = store.Get(ctx, "other")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `guardrail "other" field config.api_key`)
	})
}

func TestReencryptGuardrailSecrets(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)
		require.NoError(t, raw.Upsert(ctx, Definition{Name: "plain", Type: "presidio", Config: []byte(`{"api_key":"pk-plain"}`)}))
		require.NoError(t, sealStore(raw, box, testSecretKeys).Upsert(ctx, Definition{Name: "old-key", Type: "presidio", Config: []byte(`{"api_key":"pk-old"}`)}))
		require.NoError(t, raw.Upsert(ctx, Definition{Name: "prompt", Type: "system_prompt", Config: []byte(`{"content":"hello"}`)}))

		sealed := &sealedStore{Store: raw, box: encryptiontest.Rotate(t, keys), secretKeys: testSecretKeys}
		report, err := sealed.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, encryption.Report{Entity: "guardrail_definitions", Rows: 3, Reencrypted: 2}, report)

		for name, want := range map[string]string{"plain": "pk-plain", "old-key": "pk-old"} {
			assert.True(t, sealed.box.IsCurrent(storedConfigValues(t, raw, name)["api_key"].(string)))
			got, err := sealed.Get(ctx, name)
			require.NoError(t, err)
			assertJSONEqual(t, got.Config, `{"api_key":"`+want+`"}`)
		}

		again, err := sealed.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, again.Reencrypted)
	})
}

func TestReencryptKeepsSealedSecretsOfUnknownPlugins(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)
		require.NoError(t, sealStore(raw, box, testSecretKeys).Upsert(ctx, Definition{Name: "pii", Type: "presidio", Config: []byte(`{"api_key":"pk-old"}`)}))

		// The plugin is no longer in the catalog when the pass runs.
		noSchema := func(string) map[string]bool { return nil }
		sealed := &sealedStore{Store: raw, box: encryptiontest.Rotate(t, keys), secretKeys: noSchema}
		report, err := sealed.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, report.Reencrypted)

		stored := storedConfigValues(t, raw, "pii")["api_key"].(string)
		assert.True(t, sealed.box.IsCurrent(stored), "the secret moves to the new key instead of being written as plaintext")
		got, err := sealed.Get(ctx, "pii")
		require.NoError(t, err)
		assertJSONEqual(t, got.Config, `{"api_key":"pk-old"}`)
	})
}
