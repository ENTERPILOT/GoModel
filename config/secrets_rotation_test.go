package config

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// fakeVault is a mutable secret backend for rotation tests.
type fakeVault struct {
	mu     sync.Mutex
	values map[string]string
	err    error
}

func newFakeVault(values map[string]string) *fakeVault {
	return &fakeVault{values: values}
}

func (v *fakeVault) set(reference, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[reference] = value
}

func (v *fakeVault) fail(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

func (v *fakeVault) ResolveSecret(_ context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return "", v.err
	}
	value, ok := v.values[reference]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

func resolvedWithVault(t *testing.T, vault *fakeVault) *LoadResult {
	t.Helper()
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	result := &LoadResult{
		Config: &Config{Server: ServerConfig{MasterKey: "${vault:master}"}},
		RawProviders: map[string]RawProviderConfig{
			"openai": {APIKey: "${vault:openai}", APIKeys: []string{"literal", "${vault:openai-2}"}},
		},
		Secrets: secrets,
	}
	require.NoError(t, result.ResolveSecrets(t.Context()))
	// providers.Init resolves the providers after its env overlay.
	require.NoError(t, secrets.ResolveFields(t.Context(), "providers", &result.RawProviders))
	return result
}

func TestSecretsRecheckReportsChangedFields(t *testing.T) {
	vault := newFakeVault(map[string]string{"master": "m1", "openai": "o1", "openai-2": "o2"})
	result := resolvedWithVault(t, vault)

	recheck, err := result.Secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields(), "nothing rotated yet")

	vault.set("openai-2", "o2-new")
	vault.set("master", "m2")
	recheck, err = result.Secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"providers.openai.api_keys[1]", "server.master_key"}, recheck.Fields())
	value, ok := recheck.Value("providers.openai.api_keys[1]")
	require.True(t, ok)
	assert.Equal(t, "o2-new", value)
	_, ok = recheck.Value("providers.openai.api_key")
	assert.False(t, ok, "unchanged fields carry no value")

	t.Run("uncommitted changes are reported again", func(t *testing.T) {
		again, err := result.Secrets.Recheck(t.Context())
		require.NoError(t, err)
		assert.Equal(t, recheck.Fields(), again.Fields())
	})

	t.Run("committed changes are not", func(t *testing.T) {
		recheck.Commit()
		again, err := result.Secrets.Recheck(t.Context())
		require.NoError(t, err)
		assert.Empty(t, again.Fields())
	})
}

func TestSecretsRecheckErrorNamesFieldNotValue(t *testing.T) {
	vault := newFakeVault(map[string]string{"master": "m1", "openai": "o1", "openai-2": "o2"})
	result := resolvedWithVault(t, vault)
	vault.fail(errors.New("backend unavailable"))

	_, err := result.Secrets.Recheck(t.Context())
	require.Error(t, err)
	secretErr, ok := errors.AsType[*SecretError](err)
	require.True(t, ok)
	assert.Equal(t, "vault", secretErr.Scheme)
	require.ErrorContains(t, err, "server.master_key")
	assert.NotContains(t, err.Error(), "m1")
}

func TestSecretsRecordOnlyNamedReferencedFields(t *testing.T) {
	t.Setenv("GOMODEL_TEST_ROTATION", "v1")
	secrets := NewSecrets()

	_, err := secrets.Resolve(t.Context(), "${env:GOMODEL_TEST_ROTATION}")
	require.NoError(t, err)
	fields := map[string]string{"plain": "no reference", "legacy": "${GOMODEL_TEST_ROTATION}", "referenced": "${env:GOMODEL_TEST_ROTATION}"}
	require.NoError(t, secrets.ResolveFields(t.Context(), "", &fields))

	t.Setenv("GOMODEL_TEST_ROTATION", "v2")
	recheck, err := secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"referenced"}, recheck.Fields(), "anonymous Resolve and values without a reference are not recorded")
}

func TestSecretsRecordsDecodedExtensionFields(t *testing.T) {
	vault := newFakeVault(map[string]string{"token": "t1"})
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("client_secret: ${vault:token}\n"), &node))
	result := &LoadResult{Config: &Config{Extensions: map[string]yaml.Node{"sso": node}}, Secrets: NewSecrets()}
	require.NoError(t, result.Secrets.Register("vault", vault))
	var got map[string]string
	_, err := result.DecodeExtension("sso", &got)
	require.NoError(t, err)

	vault.set("token", "t2")
	recheck, err := result.Secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"extensions.sso.client_secret"}, recheck.Fields())
}

func TestSecretsNotifyChangedCoalesces(t *testing.T) {
	secrets := NewSecrets()
	for range 100 {
		secrets.NotifyChanged()
	}
	changes := secrets.Changes()
	select {
	case <-changes:
	default:
		t.Fatal("NotifyChanged did not signal")
	}
	select {
	case <-changes:
		t.Fatal("NotifyChanged signalled more than once for coalesced calls")
	default:
	}
}

func TestSecretsRotationNilSafe(t *testing.T) {
	var secrets *Secrets
	secrets.NotifyChanged()
	assert.Nil(t, secrets.Changes())
	recheck, err := secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields())
	recheck.Commit()

	var zero Secrets
	zero.NotifyChanged()
	assert.Len(t, zero.Changes(), 1)
}

func TestFingerprintSecretIsKeyed(t *testing.T) {
	a := fingerprintSecret("a")
	assert.Equal(t, a, fingerprintSecret("a"))
	assert.NotEqual(t, fingerprintSecret("a"), fingerprintSecret("b"))
}

func TestSecretsShareANotifierAcrossGenerations(t *testing.T) {
	notifier := NewSecretNotifier()
	serving, abandoned := NewSecrets(), NewSecrets()
	serving.SetNotifier(notifier)
	abandoned.SetNotifier(notifier)

	abandoned.NotifyChanged()
	serving.NotifyChanged()
	select {
	case <-serving.Changes():
	default:
		t.Fatal("a notification on another generation's Secrets did not reach the shared notifier")
	}
	assert.Empty(t, serving.Changes(), "notifications coalesce across generations")
}

func TestSecretFieldFromContext(t *testing.T) {
	var fields []string
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", SecretResolverFunc(func(ctx context.Context, _ string) (string, error) {
		field, ok := SecretFieldFromContext(ctx)
		if ok {
			fields = append(fields, field)
		} else {
			fields = append(fields, "<none>")
		}
		return "v", nil
	})))
	result := &LoadResult{
		Config:       &Config{Server: ServerConfig{MasterKey: "${vault:master}"}},
		RawProviders: map[string]RawProviderConfig{"openai": {APIKeys: []string{"${vault:a}"}}},
		Secrets:      secrets,
	}
	require.NoError(t, result.ResolveSecrets(t.Context()))
	require.NoError(t, secrets.ResolveFields(t.Context(), "providers", &result.RawProviders))
	_, err := secrets.Resolve(t.Context(), "${vault:anonymous}")
	require.NoError(t, err)
	assert.Equal(t, []string{"server.master_key", "providers.openai.api_keys[0]", "<none>"}, fields)

	_, ok := SecretFieldFromContext(t.Context())
	assert.False(t, ok)
}
