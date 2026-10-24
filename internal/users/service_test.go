package users

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)

	svc, err := NewService(store, testCatalog{"openai", "anthropic"})
	require.NoError(t, err)
	err = svc.Refresh(context.Background())
	require.NoError(t, err)

	return svc
}

func requestCtx(userPath string, keyAllowed ...string) context.Context {
	ctx := context.Background()
	if userPath != "" {
		ctx = core.WithEffectiveUserPath(ctx, userPath)
	}
	if len(keyAllowed) > 0 {
		ctx = core.WithCredentialAllowedModels(ctx, keyAllowed)
	}
	return ctx
}

func TestService_NoPoliciesAllowEverything(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	gpt := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	require.True(t, svc.AllowsModel(context.Background(), gpt))
	require.True(t, svc.AllowsModel(requestCtx("/acme/eng/alice"), gpt))
}

func TestService_PathAllowlistsIntersectDownTheChain(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Upsert(ctx, User{UserPath: "acme", AllowedModels: []string{"openai/*", "anthropic/*"}})
	require.NoError(t, err)
	_, err = svc.Upsert(ctx, User{UserPath: "/acme/eng", AllowedModels: []string{"anthropic/*"}})
	require.NoError(t, err)
	_, err = // A child that tries to widen its group's restriction still gets the
		// intersection: the group's allowlist must match too.
		svc.Upsert(ctx, User{UserPath: "/acme/eng/bob", AllowedModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-6"}})
	require.NoError(t, err)

	gpt := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	claude := core.ModelSelector{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	opus := core.ModelSelector{Provider: "anthropic", Model: "claude-opus-4-1"}

	tests := []struct {
		name     string
		ctx      context.Context
		selector core.ModelSelector
		want     bool
	}{
		{"group root allows openai", requestCtx("/acme"), gpt, true},
		{"group root allows anthropic", requestCtx("/acme/sales"), claude, true},
		{"eng narrows to anthropic", requestCtx("/acme/eng"), gpt, false},
		{"eng descendant inherits", requestCtx("/acme/eng/alice"), claude, true},
		{"eng descendant inherits denial", requestCtx("/acme/eng/alice"), gpt, false},
		{"bob cannot widen past eng", requestCtx("/acme/eng/bob"), gpt, false},
		{"bob narrows within eng", requestCtx("/acme/eng/bob"), opus, false},
		{"bob keeps the intersection", requestCtx("/acme/eng/bob"), claude, true},
		{"unrelated path unrestricted", requestCtx("/other"), gpt, true},
		{"key allowlist applies alone", requestCtx("", "anthropic/"), gpt, false},
		{"key allowlist intersects with path", requestCtx("/acme", "openai/"), claude, false},
		{"key and path both match", requestCtx("/acme", "openai/"), gpt, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.AllowsModel(tc.ctx, tc.selector)
			require.Equal(t, tc.want, got, "AllowsModel(%s) = %v, want %v", tc.selector.QualifiedModel(), got, tc.want)
		})
	}

	constraints := svc.Constraints("/acme/eng/bob/x")
	paths := make([]string, 0, len(constraints))
	for _, c := range constraints {
		paths = append(paths, c.UserPath)
	}
	want := []string{"/acme", "/acme/eng", "/acme/eng/bob"}
	require.Equal(t, want, paths)
}

func TestService_UpsertValidatesAndDeleteRemoves(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Upsert(ctx, User{UserPath: "", AllowedModels: []string{"openai/*"}})
	require.True(t, IsValidationError(err))
	_, err = svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"nope/*"}})
	require.True(t, IsValidationError(err))

	stored, err := svc.Upsert(ctx, User{UserPath: "acme/eng/", AllowedModels: []string{"anthropic/*", " "}, Description: " eng "})
	require.NoError(t, err)
	require.Equal(t, "/acme/eng", stored.UserPath)
	require.Equal(t, "eng", stored.Description)
	require.Equal(t, []string{"anthropic/"}, stored.AllowedModels, "stored = %#v", stored)
	got := svc.List()
	require.Len(t, got, 1)
	require.Equal(t, "/acme/eng", got[0].UserPath)
	err = svc.Delete(ctx, "/acme/eng")
	require.NoError(t, err)
	err = svc.Delete(ctx, "/acme/eng")
	require.ErrorIs(t, err, ErrNotFound)
	got = svc.List()
	require.Empty(t, got)
}

// flakyStore persists writes but can be told to fail List after the initial
// snapshot, so a failed post-write refresh is observable.
type flakyStore struct {
	Store
	failList bool
}

func (s *flakyStore) List(ctx context.Context) ([]User, error) {
	if s.failList {
		return nil, errors.New("list unavailable")
	}
	return s.Store.List(ctx)
}

func TestService_FailedRefreshStillAppliesMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner, err := NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)

	store := &flakyStore{Store: inner}
	svc, err := NewService(store, testCatalog{"openai", "anthropic"})
	require.NoError(t, err)
	_, err = svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"openai/*"}})
	require.NoError(t, err)

	gpt := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	claude := core.ModelSelector{Provider: "anthropic", Model: "claude-sonnet-4-6"}

	store.failList = true
	_, err = svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"anthropic/*"}})
	require.NoError(t, err)
	require.False(t, svc.AllowsModel(requestCtx("/acme"), gpt))
	require.True(t, svc.AllowsModel(requestCtx("/acme"), claude))
	err = svc.Delete(ctx, "/acme")
	require.NoError(t, err)
	require.True(t, svc.AllowsModel(requestCtx("/acme"), gpt))
	got := svc.List()
	require.Empty(t, got)
}

// Concurrent writes to one path, with every post-write refresh failing, must
// leave the live snapshot equal to what storage holds: the last store write
// and the last snapshot apply are the same mutation.
func TestService_ConcurrentWritesKeepSnapshotAndStoreInSync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner, err := NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)

	store := &flakyStore{Store: inner, failList: true}
	svc, err := NewService(store, testCatalog{"openai", "anthropic"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			allowed := []string{"openai/*"}
			if i%2 == 1 {
				allowed = []string{"anthropic/*"}
			}
			_, err := svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: allowed})
			assert.NoError(t, err, "Upsert(%d): %v", i, err)

		}(i)
	}
	wg.Wait()

	rows, err := inner.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	live, ok := svc.Get("/acme")
	require.True(t, ok)
	require.Equal(t, rows[0].AllowedModels, live.AllowedModels)
}

func TestService_ConfigUsersShadowStoreAndAreReadOnly(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"openai/*"}, Description: "stored"})
	require.NoError(t, err)

	svc.SetConfigUsers([]User{{UserPath: "acme", AllowedModels: []string{"anthropic/*"}, Description: "declared"}})
	err = svc.ValidateManagedConfig([]string{"anthropic"})
	require.NoError(t, err)
	err = svc.Refresh(ctx)
	require.NoError(t, err)

	got, ok := svc.Get("/acme")
	require.True(t, ok)
	require.True(t, got.Managed)
	require.Equal(t, "declared", got.Description)
	require.Equal(t, []string{"anthropic/"}, got.AllowedModels, "Get(/acme) = %#v, want managed declared row", got)
	_, err = svc.Upsert(ctx, User{UserPath: "/acme"})
	require.ErrorIs(t, err, ErrManaged)
	err = svc.Delete(ctx, "/acme")
	require.ErrorIs(t, err, ErrManaged)

	svc.SetConfigUsers([]User{{UserPath: "/acme", AllowedModels: []string{"missing/*"}}})
	require.Error(t, svc.ValidateManagedConfig([]string{"anthropic"}))
}
