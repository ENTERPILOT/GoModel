package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// credentialsInstance is one gateway instance's credentials service, with
// its own registry, over a store other instances share.
type credentialsInstance struct {
	service  *CredentialsService
	registry *ModelRegistry
}

func newCredentialsInstance(t *testing.T, store CredentialStore, declared ...string) credentialsInstance {
	t.Helper()
	registry := NewModelRegistry()
	service, err := NewCredentialsService(t.Context(), newCredentialsTestFactory(t), registry, store, declared, config.ResilienceConfig{}, nil)
	require.NoError(t, err)
	return credentialsInstance{service: service, registry: registry}
}

func TestCredentialsServiceRefreshAppliesChangesFromAnotherInstance(t *testing.T) {
	ctx := t.Context()
	store := newFakeCredentialStore()
	writer := newCredentialsInstance(t, store)
	reader := newCredentialsInstance(t, store)

	// Created elsewhere: registered here, and its models fetched.
	cred := ManagedProviderCredential{Name: "shared", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}
	require.NoError(t, writer.service.Upsert(ctx, cred))
	require.Nil(t, reader.registry.ProviderByName("shared"))
	require.NoError(t, reader.service.Refresh(ctx))
	first := reader.registry.ProviderByName("shared")
	require.NotNil(t, first)
	assert.True(t, reader.service.Installed("shared"))
	require.Eventually(t, func() bool { return reader.registry.Supports("shared/test-model") }, time.Second, 5*time.Millisecond)

	// Nothing changed: the running provider is kept.
	require.NoError(t, reader.service.Refresh(ctx))
	assert.Same(t, first, reader.registry.ProviderByName("shared"))

	// Edited elsewhere: rebuilt here.
	cred.APIKeys = []string{"sk-two"}
	require.NoError(t, writer.service.Upsert(ctx, cred))
	require.NoError(t, reader.service.Refresh(ctx))
	edited := reader.registry.ProviderByName("shared")
	require.NotNil(t, edited)
	assert.NotSame(t, first, edited)

	// Disabled elsewhere: unregistered here.
	cred.Enabled = false
	require.NoError(t, writer.service.Upsert(ctx, cred))
	require.NoError(t, reader.service.Refresh(ctx))
	assert.Nil(t, reader.registry.ProviderByName("shared"))
	assert.False(t, reader.service.Installed("shared"))
	assert.False(t, reader.registry.Supports("shared/test-model"))

	// Re-enabled, then deleted elsewhere: registered, then unregistered.
	cred.Enabled = true
	require.NoError(t, writer.service.Upsert(ctx, cred))
	require.NoError(t, reader.service.Refresh(ctx))
	require.NotNil(t, reader.registry.ProviderByName("shared"))
	require.NoError(t, writer.service.Delete(ctx, "shared"))
	require.NoError(t, reader.service.Refresh(ctx))
	assert.Nil(t, reader.registry.ProviderByName("shared"))
	assert.False(t, reader.service.Installed("shared"))
}

func TestCredentialsServiceRefreshLeavesDeclaredProvidersAlone(t *testing.T) {
	ctx := t.Context()
	store := newFakeCredentialStore()
	reader := newCredentialsInstance(t, store, "openai")
	declared := &registryMockProvider{name: "openai"}
	reader.registry.RegisterProviderWithNameAndType(declared, "openai", "openai")

	// A stored row shadowed by a declared provider never replaces it.
	require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "openai", Type: "test", APIKeys: []string{"sk"}, Enabled: true}))
	require.NoError(t, reader.service.Refresh(ctx))
	assert.Same(t, declared, reader.registry.ProviderByName("openai"))

	require.NoError(t, store.Delete(ctx, "openai"))
	require.NoError(t, reader.service.Refresh(ctx))
	assert.Same(t, declared, reader.registry.ProviderByName("openai"))
}

func TestCredentialsServiceRefreshKeepsRunningProviderWhenEditDoesNotBuild(t *testing.T) {
	ctx := t.Context()
	store := newFakeCredentialStore()
	reader := newCredentialsInstance(t, store)
	require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "flaky", Type: "test", APIKeys: []string{"sk"}, Enabled: true}))
	require.NoError(t, reader.service.Refresh(ctx))
	running := reader.registry.ProviderByName("flaky")
	require.NotNil(t, running)

	// Stored without a key, which this type needs: it cannot build here.
	require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "flaky", Type: "test", Enabled: true}))
	require.NoError(t, reader.service.Refresh(ctx))
	assert.Same(t, running, reader.registry.ProviderByName("flaky"))
	assert.Contains(t, reader.service.Skipped(), "flaky")
}

// A save's own refresh, after its store round trip, must not rebuild the
// provider it just installed: a rebuild drops its models until they are
// fetched again.
func TestCredentialsServiceRefreshAfterLocalSaveKeepsProvider(t *testing.T) {
	check := func(t *testing.T, store CredentialStore) {
		ctx := t.Context()
		writer := newCredentialsInstance(t, store)
		require.NoError(t, writer.service.Upsert(ctx, ManagedProviderCredential{Name: "local", Type: "test", APIKeys: []string{"sk"}, Enabled: true}))
		installed := writer.registry.ProviderByName("local")
		require.NotNil(t, installed)
		require.NoError(t, writer.service.Refresh(ctx))
		assert.Same(t, installed, writer.registry.ProviderByName("local"))
	}
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLCredentialStore(t.Context(), db)
		require.NoError(t, err)
		check(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBCredentialStore(db)
		require.NoError(t, err)
		check(t, store)
	})
}

func TestCredentialsServiceAnnouncesSavesAndDeletes(t *testing.T) {
	ctx := t.Context()
	writer := newCredentialsInstance(t, newFakeCredentialStore())
	var announced int
	writer.service.SetChangeNotifier(func(context.Context) { announced++ })

	cred := ManagedProviderCredential{Name: "p", Type: "test", APIKeys: []string{"sk"}, Enabled: true}
	require.NoError(t, writer.service.Upsert(ctx, cred))
	require.NoError(t, writer.service.Delete(ctx, "p"))
	assert.Equal(t, 2, announced)

	// A rejected save changed nothing, so there is nothing to announce.
	require.Error(t, writer.service.Upsert(ctx, ManagedProviderCredential{Name: "p", Type: "unknown", Enabled: true}))
	assert.Equal(t, 2, announced)
}

// hookedListStore runs onList, once, after the next List has read the rows.
// List fails with listErr while it is set.
type hookedListStore struct {
	*fakeCredentialStore
	onList  func()
	listErr error
}

func (s *hookedListStore) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	rows, err := s.fakeCredentialStore.List(ctx)
	if hook := s.onList; hook != nil {
		s.onList = nil
		hook()
	}
	return rows, err
}

// A local rotation that lands while a refresh reads the rows makes it skip
// the remote change it read; the refresh must still apply it before it
// reports success, or the change is marked applied and never retried.
func TestCredentialsServiceRefreshAppliesRemoteChangeAfterConcurrentRotation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, writer *CredentialsService)
		check  func(t *testing.T, reader credentialsInstance)
	}{
		{
			name: "edit",
			change: func(t *testing.T, writer *CredentialsService) {
				require.NoError(t, writer.Upsert(t.Context(), ManagedProviderCredential{Name: "shared", Type: "test", APIKeys: []string{"sk-two"}, Enabled: true}))
			},
			check: func(t *testing.T, reader credentialsInstance) {
				reader.service.mu.RLock()
				defer reader.service.mu.RUnlock()
				assert.Equal(t, "sk-two", reader.service.keyrings["shared"].Primary())
			},
		},
		{
			name: "delete",
			change: func(t *testing.T, writer *CredentialsService) {
				require.NoError(t, writer.Delete(t.Context(), "shared"))
			},
			check: func(t *testing.T, reader credentialsInstance) {
				assert.Nil(t, reader.registry.ProviderByName("shared"))
				assert.False(t, reader.service.Installed("shared"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := &hookedListStore{fakeCredentialStore: newFakeCredentialStore()}
			writer := newCredentialsInstance(t, store)
			require.NoError(t, writer.service.Upsert(ctx, ManagedProviderCredential{Name: "shared", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}))
			reader := newCredentialsInstance(t, store)
			require.NotNil(t, reader.registry.ProviderByName("shared"))

			tc.change(t, writer.service)
			// What an in-place key swap by rotation does, between the
			// refresh taking its revisions and installing what it read.
			store.onList = func() {
				reader.service.mu.Lock()
				reader.service.revisions["shared"]++
				reader.service.mu.Unlock()
			}
			require.NoError(t, reader.service.Refresh(ctx))
			tc.check(t, reader)
		})
	}
}

// A refresh whose retry pass fails has still installed what the first pass
// read. Those providers' models must be fetched anyway: the next refresh sees
// them running and installs nothing, so it would never fetch them.
func TestCredentialsServiceRefreshFetchesModelsWhenRetryPassFails(t *testing.T) {
	ctx := t.Context()
	store := &hookedListStore{fakeCredentialStore: newFakeCredentialStore()}
	writer := newCredentialsInstance(t, store)
	require.NoError(t, writer.service.Upsert(ctx, ManagedProviderCredential{Name: "rotated", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}))
	reader := newCredentialsInstance(t, store)
	// Let the startup model fetch finish, so only the refresh can fetch
	// models for what it installs.
	require.Eventually(t, func() bool { return reader.registry.Supports("rotated/test-model") }, time.Second, 5*time.Millisecond)

	require.NoError(t, writer.service.Upsert(ctx, ManagedProviderCredential{Name: "rotated", Type: "test", APIKeys: []string{"sk-two"}, Enabled: true}))
	require.NoError(t, writer.service.Upsert(ctx, ManagedProviderCredential{Name: "fresh", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}))
	// A local rotation of "rotated" makes the first pass skip it, and the
	// store is unreachable for the retry pass.
	store.onList = func() {
		reader.service.mu.Lock()
		reader.service.revisions["rotated"]++
		reader.service.mu.Unlock()
		store.listErr = errors.New("store unavailable")
	}
	require.Error(t, reader.service.Refresh(ctx))
	require.NotNil(t, reader.registry.ProviderByName("fresh"))
	require.Eventually(t, func() bool { return reader.registry.Supports("fresh/test-model") }, time.Second, 5*time.Millisecond)

	store.listErr = nil
	require.NoError(t, reader.service.Refresh(ctx))
	assert.True(t, reader.registry.Supports("fresh/test-model"))
}
