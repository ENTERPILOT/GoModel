package workflows

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Interface-level behaviour, asserted once and run on every backend through
// runStoreSuite. Backend-specific cases live in store_sql_test.go.

func TestStoreCreateRoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		created, err := store.Create(ctx, CreateInput{
			Scope:       Scope{Provider: " openai ", Model: "gpt-5", UserPath: "/team/a"},
			Name:        " scoped ",
			Description: " routes team a ",
			Payload:     testWorkflowPayload(),
			Activate:    true,
		})
		require.NoError(t, err)
		require.NotEmpty(t, created.ID)
		assert.NotEmpty(t, created.WorkflowHash)

		got, err := store.Get(ctx, created.ID)
		require.NoError(t, err)

		assert.Equal(t, created.ID, got.ID)
		assert.Equal(t, Scope{Provider: "openai", Model: "gpt-5", UserPath: "/team/a"}, got.Scope)
		assert.Equal(t, "provider_model_path:openai:gpt-5:/team/a", got.ScopeKey)
		assert.Equal(t, 1, got.Version)
		assert.True(t, got.Active)
		assert.False(t, got.Managed)
		assert.Equal(t, "scoped", got.Name)
		assert.Equal(t, "routes team a", got.Description)
		assert.Equal(t, created.WorkflowHash, got.WorkflowHash)
		// Backends differ in timestamp precision (unix seconds vs milliseconds);
		// the instant must survive, its sub-second part need not.
		assert.WithinDuration(t, created.CreatedAt, got.CreatedAt, time.Second)
		assert.Equal(t, time.UTC, got.CreatedAt.Location())

		wantPayload, err := json.Marshal(created.Payload)
		require.NoError(t, err)
		gotPayload, err := json.Marshal(got.Payload)
		require.NoError(t, err)
		assert.JSONEq(t, string(wantPayload), string(gotPayload))
	})
}

func TestStoreGetMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		_, err := store.Get(context.Background(), "missing")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreCreateAllocatesVersionsAndDeactivatesPrevious(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		first, err := store.Create(ctx, CreateInput{
			Name: "first", Payload: testWorkflowPayload(), Activate: true,
		})
		require.NoError(t, err)

		second, err := store.Create(ctx, CreateInput{
			Name: "second", Payload: testWorkflowPayload(), Activate: true,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, first.Version)
		assert.Equal(t, 2, second.Version)

		// Activating a new version must retire the previous one: the unique
		// partial index allows only one active row per scope.
		active, err := store.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
		require.Equal(t, second.ID, active[0].ID)

		retired, err := store.Get(ctx, first.ID)
		require.NoError(t, err)
		assert.False(t, retired.Active)

		// Versions are numbered per scope, and an inactive create leaves the
		// scope's active version alone.
		scoped, err := store.Create(ctx, CreateInput{
			Scope: Scope{Provider: "openai"}, Name: "scoped", Payload: testWorkflowPayload(),
		})
		require.NoError(t, err)
		assert.Equal(t, 1, scoped.Version)
		assert.False(t, scoped.Active)

		active, err = store.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, second.ID, active[0].ID)
	})
}

func TestStoreListActiveExcludesDeactivatedAndOrdersNewestFirst(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		var created []*Version
		for _, scope := range []Scope{{}, {Provider: "openai"}, {UserPath: "/team/a"}, {Provider: "groq"}} {
			version, err := store.Create(ctx, CreateInput{
				Scope: scope, Name: "active", Payload: testWorkflowPayload(), Activate: true,
			})
			require.NoError(t, err)
			created = append(created, version)
		}
		require.NoError(t, store.Deactivate(ctx, created[1].ID))

		active, err := store.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, active, 3)

		ids := make([]string, 0, len(active))
		for _, version := range active {
			ids = append(ids, version.ID)
		}
		assert.ElementsMatch(t, []string{created[0].ID, created[2].ID, created[3].ID}, ids)

		// Newest first, id descending as the tie-break. Creates within the
		// same clock tick share a created_at, so the invariant is asserted
		// on the returned rows rather than on a fixed expected order.
		sorted := sort.SliceIsSorted(active, func(i, j int) bool {
			if !active[i].CreatedAt.Equal(active[j].CreatedAt) {
				return active[i].CreatedAt.After(active[j].CreatedAt)
			}
			return active[i].ID > active[j].ID
		})
		assert.True(t, sorted, "ListActive order = %v", ids)
	})
}

func TestStoreDeactivate(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		created, err := store.Create(ctx, CreateInput{
			Name: "only", Payload: testWorkflowPayload(), Activate: true,
		})
		require.NoError(t, err)
		err = store.Deactivate(ctx, created.ID)
		require.NoError(t, err)
		// Deactivating twice reports not-found rather than silently succeeding.
		err = store.Deactivate(ctx, created.ID)
		require.ErrorIs(t, err, ErrNotFound)
		assert.ErrorIs(t, store.Deactivate(ctx, "missing"), ErrNotFound)

		got, err := store.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.False(t, got.Active)

		active, err := store.ListActive(ctx)
		require.NoError(t, err)
		assert.Empty(t, active)
	})
}

func TestStoreEnsureManagedDefaultGlobalIsIdempotent(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		input := CreateInput{
			Name:        ManagedDefaultGlobalName,
			Description: ManagedDefaultGlobalDescription,
			Payload:     testWorkflowPayload(),
			Managed:     true,
			Activate:    true,
		}
		created, err := store.EnsureManagedDefaultGlobal(ctx, input, "hash-1")
		require.NoError(t, err)
		require.NotNil(t, created)
		assert.Equal(t, 1, created.Version)
		assert.True(t, created.Managed)
		assert.True(t, created.Active)
		assert.Equal(t, "global", created.ScopeKey)
		assert.Equal(t, "hash-1", created.WorkflowHash)

		// Same hash: nothing new is published on the next start.
		again, err := store.EnsureManagedDefaultGlobal(ctx, input, "hash-1")
		require.NoError(t, err)
		assert.Nil(t, again)

		// A changed hash publishes a new version and retires the old one.
		updated, err := store.EnsureManagedDefaultGlobal(ctx, input, "hash-2")
		require.NoError(t, err)
		require.NotNil(t, updated)
		require.Equal(t, 2, updated.Version)
		assert.Equal(t, "hash-2", updated.WorkflowHash)

		previous, err := store.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.False(t, previous.Active)

		active, err := store.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, updated.ID, active[0].ID)
	})
}

func TestStoreEnsureManagedDefaultGlobalLeavesOperatorVersionAlone(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		operator, err := store.Create(ctx, CreateInput{
			Name: "operator authored", Payload: testWorkflowPayload(), Activate: true,
		})
		require.NoError(t, err)

		published, err := store.EnsureManagedDefaultGlobal(ctx, CreateInput{
			Name:        ManagedDefaultGlobalName,
			Description: ManagedDefaultGlobalDescription,
			Payload:     testWorkflowPayload(),
			Managed:     true,
			Activate:    true,
		}, "hash-1")
		require.NoError(t, err)
		assert.Nil(t, published)

		active, err := store.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, operator.ID, active[0].ID)
	})
}
