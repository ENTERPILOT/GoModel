package app

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/providers"
)

// storeAwareComponent plays every extension role, so one value can be
// registered several times.
type storeAwareComponent struct {
	store ext.SharedStore
	calls int
}

func (*storeAwareComponent) Name() string { return "store-aware" }
func (*storeAwareComponent) AuthenticateRequest(context.Context, *http.Request) (*ext.Authentication, error) {
	return nil, nil
}
func (*storeAwareComponent) Rewrite(context.Context, ext.Input) (*ext.Result, error) {
	return nil, nil
}
func (*storeAwareComponent) CheckHealth(context.Context) ext.HealthStatus { return ext.HealthOK }
func (*storeAwareComponent) Select(ext.RouteRequest) (string, bool)       { return "", false }
func (*storeAwareComponent) OnAttemptStart(ext.RouteTarget)               {}
func (*storeAwareComponent) OnAttemptEnd(ext.RouteOutcome)                {}
func (s *storeAwareComponent) SetSharedStore(store ext.SharedStore) {
	s.store = store
	s.calls++
}

type storeAwareSetting struct{ storeAwareComponent }

func (*storeAwareSetting) Descriptor() ext.SettingDescriptor {
	return ext.SettingDescriptor{Key: "test.store_aware", Value: "fixed", Locked: true}
}
func (*storeAwareSetting) Apply(string) error { return nil }

type fakeSharedStore struct{ ext.SharedStore }

func TestBindSharedStoreReachesEveryRoleOnce(t *testing.T) {
	registry := &ext.Registry{}
	everywhere := &storeAwareComponent{}
	registry.RegisterAuthenticator(everywhere)
	registry.RegisterRewriter(everywhere)
	registry.RegisterHealthChecker(everywhere)
	registry.RegisterRouteSelector(everywhere)
	rewriter := &storeAwareComponent{}
	registry.RegisterRewriter(rewriter)
	setting := &storeAwareSetting{}
	registry.RegisterSetting(setting)
	var typedNil *storeAwareComponent
	registry.RegisterAuthenticator(typedNil)
	registry.RegisterAuthenticator(&appTestAuthenticator{})

	store := &fakeSharedStore{}
	bindSharedStore(registry, store)
	for name, component := range map[string]*storeAwareComponent{
		"multi-role": everywhere, "rewriter": rewriter, "setting": &setting.storeAwareComponent,
	} {
		assert.Same(t, store, component.store, name)
		assert.Equal(t, 1, component.calls, name)
	}

	var nilStore *fakeSharedStore
	bindSharedStore(registry, nilStore)
	bindSharedStore(nil, store)
	assert.Equal(t, 1, everywhere.calls, "a nil store or registry must not rebind")
}

func loadSharedStoreTestConfig(t *testing.T) *config.LoadResult {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "shared-store.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	loaded, err := config.Load()
	require.NoError(t, err)
	return loaded
}

func TestNoSharedStoreWithoutAwareComponent(t *testing.T) {
	loaded := loadSharedStoreTestConfig(t)
	registry := &ext.Registry{}
	registry.RegisterAuthenticator(&appTestAuthenticator{})
	application, err := New(context.Background(), Config{
		AppConfig: loaded, Factory: providers.NewProviderFactory(), Extensions: registry,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	assert.Nil(t, application.sharedStore)
}

// A reload builds the next generation before the serving one shuts down. The
// component must end up on the newest store, and the old generation's
// shutdown must not break it.
func TestSharedStoreFollowsGenerations(t *testing.T) {
	ctx := context.Background()
	loaded := loadSharedStoreTestConfig(t)
	component := &storeAwareComponent{}
	registry := &ext.Registry{}
	registry.RegisterAuthenticator(component)

	first, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory(), Extensions: registry})
	require.NoError(t, err)
	require.NotNil(t, component.store)
	firstStore := component.store
	require.NoError(t, firstStore.Put(ctx, "test.ns", "k", []byte("v"), time.Now().Add(time.Hour)))

	reloaded, err := config.Load()
	require.NoError(t, err)
	second, err := New(ctx, Config{AppConfig: reloaded, Factory: providers.NewProviderFactory(), Extensions: registry, Replaces: first})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Shutdown(ctx) })
	require.NotSame(t, firstStore, component.store)
	assert.Equal(t, 2, component.calls)

	require.NoError(t, first.Shutdown(ctx))
	_, _, err = firstStore.Get(ctx, "test.ns", "k")
	require.ErrorIs(t, err, ext.ErrSharedStoreClosed)

	// The entry is durable: the next generation reads it from the database.
	value, found, err := component.store.Get(ctx, "test.ns", "k")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("v"), value)

	require.NoError(t, second.Shutdown(ctx))
	_, err = component.store.List(ctx, "test.ns")
	assert.ErrorIs(t, err, ext.ErrSharedStoreClosed)
}

func TestFailedConstructionDoesNotRebindSharedStore(t *testing.T) {
	loaded := loadSharedStoreTestConfig(t)
	// Fail late in construction, as in the authentication-recorder case.
	enabled := true
	loaded.Config.Cache.Response.Semantic = &config.SemanticCacheConfig{
		Enabled:  &enabled,
		Embedder: config.EmbedderConfig{Provider: "missing", Model: "embedding-model"},
	}
	serving := &fakeSharedStore{}
	component := &storeAwareComponent{store: serving}
	registry := &ext.Registry{}
	registry.RegisterAuthenticator(component)

	application, err := New(context.Background(), Config{
		AppConfig: loaded, Factory: providers.NewProviderFactory(), Extensions: registry,
	})
	if err == nil {
		_ = application.Shutdown(context.Background())
	}
	require.Error(t, err)
	require.Same(t, serving, component.store, "failed construction replaced the serving generation's shared store")
	assert.Zero(t, component.calls)
}
