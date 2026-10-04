package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

func secretsTestConfig(t *testing.T, key string) *config.LoadResult {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "secrets.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("GOMODEL_ENCRYPTION_KEY", key)
	loaded, err := config.Load()
	require.NoError(t, err)
	return loaded
}

// withSQL opens the configured SQLite database for seeding and inspection.
func withSQL(t *testing.T, loaded *config.LoadResult, fn func(sqlx.DB)) {
	t.Helper()
	shared, err := storage.New(context.Background(), loaded.Config.Storage.BackendConfig())
	require.NoError(t, err)
	defer func() { _ = shared.Close() }()
	db, err := sqlx.NewSQLite(shared.(storage.SQLiteStorage).DB())
	require.NoError(t, err)
	fn(db)
}

func seedPlaintextSecrets(t *testing.T, db sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	creds, err := providers.NewSQLCredentialStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, creds.Upsert(ctx, providers.ManagedProviderCredential{Name: "openai", Type: "openai", APIKeys: []string{"sk-plain"}, Enabled: true}))
	servers, err := mcpgateway.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, servers.Upsert(ctx, mcpgateway.ManagedServer{Name: "github", URL: "https://mcp.example.com", Transport: "http", Headers: map[string]string{"Authorization": "Bearer plain"}, Enabled: true}))
	defs, err := guardrails.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, defs.Upsert(ctx, guardrails.Definition{Name: "pii", Type: "presidio", Config: []byte(`{"api_key":"pk-plain","language":"en"}`)}))
}

type storedSecrets struct {
	APIKey, Header, GuardrailKey string
}

func readStoredSecrets(t *testing.T, db sqlx.DB) storedSecrets {
	t.Helper()
	ctx := context.Background()
	var out storedSecrets
	var apiKeys, guardrailConfig string
	require.NoError(t, db.QueryRow(ctx, `SELECT api_keys FROM provider_credentials WHERE name = 'openai'`).Scan(&apiKeys))
	var keys []string
	require.NoError(t, json.Unmarshal([]byte(apiKeys), &keys))
	out.APIKey = keys[0]
	var headersJSON string
	require.NoError(t, db.QueryRow(ctx, `SELECT headers FROM mcp_servers WHERE name = 'github'`).Scan(&headersJSON))
	var headers map[string]string
	require.NoError(t, json.Unmarshal([]byte(headersJSON), &headers))
	out.Header = headers["Authorization"]
	require.NoError(t, db.QueryRow(ctx, `SELECT config FROM guardrail_definitions WHERE name = 'pii'`).Scan(&guardrailConfig))
	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(guardrailConfig), &cfg))
	out.GuardrailKey = cfg["api_key"].(string)
	return out
}

func TestReencryptSecrets(t *testing.T) {
	previousSettle := rotationSettle
	rotationSettle = 0
	t.Cleanup(func() { rotationSettle = previousSettle })
	loaded := secretsTestConfig(t, "test-key")
	withSQL(t, loaded, func(db sqlx.DB) { seedPlaintextSecrets(t, db) })
	ctx := context.Background()

	result, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded})
	require.NoError(t, err)
	assert.Equal(t, "1", result.ActiveKeyID)
	assert.Equal(t, []encryption.Report{
		{Entity: "provider_credentials", Rows: 1, Reencrypted: 1},
		{Entity: "mcp_servers", Rows: 1, Reencrypted: 1},
		{Entity: "guardrail_definitions", Rows: 1, Reencrypted: 1},
	}, result.Reports)

	withSQL(t, loaded, func(db sqlx.DB) {
		stored := readStoredSecrets(t, db)
		for _, value := range []string{stored.APIKey, stored.Header, stored.GuardrailKey} {
			assert.Regexp(t, `^enc:v1:1:`, value)
		}
	})

	again, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded})
	require.NoError(t, err)
	for _, report := range again.Reports {
		assert.Zero(t, report.Reencrypted, "%s: a second pass rewrites nothing", report.Entity)
	}

	rotated, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded, RotateDataKey: true})
	require.NoError(t, err)
	assert.Equal(t, "2", rotated.ActiveKeyID)
	for _, report := range rotated.Reports {
		assert.Equal(t, 1, report.Reencrypted, "%s moves to the new data key", report.Entity)
	}
	withSQL(t, loaded, func(db sqlx.DB) {
		assert.Regexp(t, `^enc:v1:2:`, readStoredSecrets(t, db).APIKey)
	})
}

func TestReencryptSecretsNeedsKey(t *testing.T) {
	loaded := secretsTestConfig(t, "")
	_, err := ReencryptSecrets(context.Background(), ReencryptOptions{Config: loaded})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOMODEL_ENCRYPTION_KEY is not set")
}

func TestNewReadsEncryptedSecretsAndRejectsWrongKey(t *testing.T) {
	loaded := secretsTestConfig(t, "right-key")
	withSQL(t, loaded, func(db sqlx.DB) { seedPlaintextSecrets(t, db) })
	ctx := context.Background()
	_, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded})
	require.NoError(t, err)

	application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.NoError(t, err)
	creds, err := application.providerCredentials.Store.Get(ctx, "openai")
	require.NoError(t, err)
	assert.Equal(t, []string{"sk-plain"}, creds.APIKeys, "the gateway reads the encrypted credential")
	require.NoError(t, application.Shutdown(ctx))

	loaded.Config.Storage.EncryptionKey = "wrong-key"
	_, err = New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load encryption keys")
	assert.Contains(t, err.Error(), "GOMODEL_ENCRYPTION_KEY_PREVIOUS")

	loaded.Config.Storage.EncryptionKey = ""
	_, err = New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOMODEL_ENCRYPTION_KEY is not set")
}

// testKeyWrapper is a KMS stand-in with a fixed AES key.
type testKeyWrapper struct{ aead cipher.AEAD }

func newTestKeyWrapper(t *testing.T) *testKeyWrapper {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return &testKeyWrapper{aead: aead}
}

func (w *testKeyWrapper) ID() string { return "kms:test" }

func (w *testKeyWrapper) WrapKey(_ context.Context, dek []byte) ([]byte, error) {
	nonce := make([]byte, w.aead.NonceSize())
	return w.aead.Seal(nonce, nonce, dek, nil), nil
}

func (w *testKeyWrapper) UnwrapKey(_ context.Context, wrapped []byte) ([]byte, error) {
	n := w.aead.NonceSize()
	return w.aead.Open(nil, wrapped[:n], wrapped[n:], nil)
}

func TestOpenSecretBoxUsesKeyWrapperFromLoadResult(t *testing.T) {
	loaded := secretsTestConfig(t, "")
	loaded.SetKeyWrapper(newTestKeyWrapper(t))
	ctx := context.Background()

	shared, err := storage.New(ctx, loaded.Config.Storage.BackendConfig())
	require.NoError(t, err)
	defer func() { _ = shared.Close() }()

	box, err := openSecretBox(ctx, shared, loaded)
	require.NoError(t, err)
	assert.True(t, box.Enabled(), "a key wrapper enables encryption without GOMODEL_ENCRYPTION_KEY")

	keys, err := encryption.NewKeyStore(ctx, shared)
	require.NoError(t, err)
	stored, err := keys.List(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "kms:test", stored[0].WrapperID)
}

func TestReencryptSecretsReportsOnlyPassesThatRan(t *testing.T) {
	loaded := secretsTestConfig(t, "test-key")
	ctx := context.Background()
	_, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded})
	require.NoError(t, err)
	withSQL(t, loaded, func(db sqlx.DB) {
		seedPlaintextSecrets(t, db)
		// A value under a data key the database does not have makes the
		// first pass fail.
		_, err := db.Exec(ctx, `UPDATE provider_credentials SET api_keys = ? WHERE name = 'openai'`,
			`["enc:v1:9:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]`)
		require.NoError(t, err)
	})

	result, err := ReencryptSecrets(ctx, ReencryptOptions{Config: loaded})
	require.Error(t, err)
	require.Len(t, result.Reports, 1, "passes that did not run have no report")
	assert.Equal(t, "provider_credentials", result.Reports[0].Entity)
}
