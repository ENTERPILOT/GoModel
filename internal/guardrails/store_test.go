package guardrails

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

// assertJSONEqual compares configs semantically: PostgreSQL's JSONB and the
// MongoDB document store both reorder keys and drop whitespace.
func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	require.JSONEq(t, want, string(got))
}

func TestStoreRoundTripsEveryField(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		created := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		start := time.Now().Truncate(time.Second)

		scoped := Definition{
			Name:        "scoped",
			Type:        "system_prompt",
			Description: "team prompt",
			UserPath:    "/team/alpha",
			Config:      []byte(`{"content": "be concise", "max_tokens": 100, "tags": ["a", "b"]}`),
			FailMode:    "open",
			TimeoutMS:   1500,
			CreatedAt:   created,
		}
		global := Definition{Name: "global", Type: "system_prompt", Config: []byte(`{"content":"y"}`)}
		for _, definition := range []Definition{scoped, global} {
			err := store.Upsert(ctx, definition)
			require.NoError(t, err, "Upsert(%s)", definition.Name)
		}

		got, err := store.Get(ctx, "scoped")
		require.NoError(t, err)
		assert.Equal(t, "scoped", got.Name)
		assert.Equal(t, "system_prompt", got.Type)
		assert.Equal(t, "team prompt", got.Description)
		assert.Equal(t, "/team/alpha", got.UserPath)
		assert.Equal(t, "open", got.FailMode)
		assert.Equal(t, 1500, got.TimeoutMS)
		assertJSONEqual(t, got.Config, `{"content":"be concise","max_tokens":100,"tags":["a","b"]}`)
		// A caller-supplied created_at is kept; updated_at is always stamped
		// by the store. Both come back in UTC.
		assert.True(t, got.CreatedAt.Equal(created), "CreatedAt = %v, want %v", got.CreatedAt, created)
		assert.False(t, got.UpdatedAt.Before(start), "UpdatedAt = %v, want >= %v", got.UpdatedAt, start)
		assert.Equal(t, time.UTC, got.CreatedAt.Location(), "CreatedAt not UTC")
		assert.Equal(t, time.UTC, got.UpdatedAt.Location(), "UpdatedAt not UTC")

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, definitions, 2)
		// Ordered by name ascending.
		require.Equal(t, "global", definitions[0].Name)
		require.Equal(t, "scoped", definitions[1].Name)
		assert.Empty(t, definitions[0].UserPath, "global row carries non-default user path: %+v", definitions[0])
		assert.Empty(t, definitions[0].Description, "global row carries non-default description: %+v", definitions[0])
		assert.Empty(t, definitions[0].FailMode, "global row carries non-default fail mode: %+v", definitions[0])
		assert.Zero(t, definitions[0].TimeoutMS, "global row carries non-default timeout: %+v", definitions[0])
		assert.False(t, definitions[0].CreatedAt.IsZero(), "global CreatedAt not stamped")
		assert.False(t, definitions[0].CreatedAt.Before(start), "global CreatedAt = %v, want stamped >= %v", definitions[0].CreatedAt, start)
		assert.Equal(t, *got, definitions[1], "List row should match Get result")
	})
}

func TestStoreListOnEmptyStore(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		definitions, err := store.List(context.Background())
		require.NoError(t, err)
		require.NotNil(t, definitions, "List should return an empty non-nil slice")
		require.Empty(t, definitions)
	})
}

func TestStoreListOrdersByName(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, name := range []string{"charlie", "alpha", "bravo"} {
			err := store.Upsert(ctx, Definition{Name: name, Type: "system_prompt"})
			require.NoError(t, err, "Upsert(%s)", name)
		}
		definitions, err := store.List(ctx)
		require.NoError(t, err)

		names := make([]string, 0, len(definitions))
		for _, definition := range definitions {
			names = append(names, definition.Name)
		}
		require.Equal(t, []string{"alpha", "bravo", "charlie"}, names)
	})
}

func TestStoreUpsertNormalizesIdentity(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		err := store.Upsert(ctx, Definition{
			Name:        "  padded  ",
			Type:        " System-Prompt ",
			Description: "  desc  ",
			UserPath:    "team/alpha/",
			FailMode:    " Fail-Open ",
			Config:      []byte(`{"content":"c"}`),
		})
		require.NoError(t, err)

		got, err := store.Get(ctx, "padded")
		require.NoError(t, err, "Get(padded)")
		require.Equal(t, "padded", got.Name)
		require.Equal(t, "system_prompt", got.Type)
		require.Equal(t, "desc", got.Description)
		require.Equal(t, "/team/alpha", got.UserPath)
		require.Equal(t, "open", got.FailMode)
		// Get trims the lookup name too.
		_, err = store.Get(ctx, "  padded ")
		require.NoError(t, err, "Get(padded name)")
	})
}

// TestStoreEmptyConfigNormalizesToEmptyObject pins that a definition saved
// without a config reads back as {} rather than an empty string.
func TestStoreEmptyConfigNormalizesToEmptyObject(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, config := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("   ")} {
			err := store.Upsert(ctx, Definition{Name: "empty", Type: "system_prompt", Config: config})
			require.NoError(t, err, "Upsert(%q)", config)

			got, err := store.Get(ctx, "empty")
			require.NoError(t, err)
			assertJSONEqual(t, got.Config, `{}`)
		}
	})
}

// TestStoreNullConfigRoundTrip pins the SQL behaviour for a literal JSON null
// config: it is stored and returned verbatim. The MongoDB store normalizes
// null to {} on write (see mongoConfigFromRaw), so the backends drift here.
func TestStoreNullConfigRoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		if _, isMongo := store.(*MongoDBStore); isMongo {
			t.Skipf("drift: MongoDBStore normalizes a null config to {} while SQLStore returns null verbatim")
		}
		ctx := context.Background()
		err := store.Upsert(ctx, Definition{Name: "null", Type: "system_prompt", Config: json.RawMessage("null")})
		require.NoError(t, err)

		got, err := store.Get(ctx, "null")
		require.NoError(t, err)
		assertJSONEqual(t, got.Config, `null`)
	})
}

func TestStoreUpsertOverwritesAndPreservesCreatedAt(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.Upsert(ctx, Definition{
			Name: "g", Type: "system_prompt", Description: "first", UserPath: "/team/alpha",
			FailMode: "open", TimeoutMS: 1500, Config: []byte(`{"content":"c"}`),
		})
		require.NoError(t, err, "first Upsert")

		created, err := store.Get(ctx, "g")
		require.NoError(t, err)

		// A re-upsert replaces every field but created_at; optional fields
		// left empty fall back to their defaults rather than lingering.
		err = store.Upsert(ctx, Definition{Name: "g", Type: "llm_based_altering", Config: []byte(`{"model":"openai/gpt-4o"}`)})
		require.NoError(t, err, "second Upsert")

		updated, err := store.Get(ctx, "g")
		require.NoError(t, err, "Get after update")
		assert.Equal(t, "llm_based_altering", updated.Type)
		assertJSONEqual(t, updated.Config, `{"model":"openai/gpt-4o"}`)
		assert.Empty(t, updated.Description, "description not reset: %+v", updated)
		assert.Empty(t, updated.UserPath, "user path not reset: %+v", updated)
		assert.Empty(t, updated.FailMode, "fail mode not reset: %+v", updated)
		assert.Zero(t, updated.TimeoutMS, "timeout not reset: %+v", updated)
		assert.True(t, updated.CreatedAt.Equal(created.CreatedAt), "CreatedAt = %v, want %v preserved", updated.CreatedAt, created.CreatedAt)
		assert.False(t, updated.UpdatedAt.Before(created.UpdatedAt), "UpdatedAt = %v, want >= %v", updated.UpdatedAt, created.UpdatedAt)

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		assert.Len(t, definitions, 1, "after re-upsert")
	})
}

func TestStoreUpsertRejectsInvalidDefinition(t *testing.T) {
	cases := map[string]Definition{
		"empty name":       {Name: "   ", Type: "system_prompt"},
		"slash in name":    {Name: "team/prompt", Type: "system_prompt"},
		"empty type":       {Name: "g", Type: " "},
		"bad fail mode":    {Name: "g", Type: "system_prompt", FailMode: "sideways"},
		"negative timeout": {Name: "g", Type: "system_prompt", TimeoutMS: -1},
		"bad user path":    {Name: "g", Type: "system_prompt", UserPath: "/team/../x"},
	}
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for name, definition := range cases {
			err := store.Upsert(ctx, definition)
			assert.True(t, IsValidationError(err), "%s: Upsert error = %v, want validation error", name, err)
		}
		definitions, err := store.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, definitions, "after rejected upserts")
	})
}

func TestStoreUpsertManyIsAtomic(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		// The second definition is invalid, so nothing from the batch should
		// land: config seeding must not half-apply.
		err := store.UpsertMany(ctx, []Definition{
			{Name: "valid", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
			{Name: "", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
		})
		require.True(t, IsValidationError(err), "UpsertMany with an invalid definition error = %v, want validation error", err)

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, definitions, "after failed batch")
	})
}

func TestStoreUpsertManyCommits(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.UpsertMany(ctx, nil)
		require.NoError(t, err, "UpsertMany(nil)")
		err = store.Upsert(ctx, Definition{Name: "a", Type: "system_prompt", Description: "old", Config: []byte(`{"content":"old"}`)})
		require.NoError(t, err, "Upsert(a)")

		existing, err := store.Get(ctx, "a")
		require.NoError(t, err, "Get(a)")

		err = store.UpsertMany(ctx, []Definition{
			{Name: "a", Type: "system_prompt", Config: []byte(`{"content":"new"}`)},
			{Name: "b", Type: "system_prompt", UserPath: "/team/beta", Config: []byte(`{"content":"c"}`)},
		})
		require.NoError(t, err)

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, definitions, 2)
		require.Equal(t, "a", definitions[0].Name)
		require.Equal(t, "b", definitions[1].Name)
		// The batch overwrites like Upsert does: fields replaced, created_at kept.
		assertJSONEqual(t, definitions[0].Config, `{"content":"new"}`)
		assert.Empty(t, definitions[0].Description, "a after batch = %+v, want description reset", definitions[0])
		assert.True(t, definitions[0].CreatedAt.Equal(existing.CreatedAt), "a after batch = %+v, want CreatedAt %v", definitions[0], existing.CreatedAt)
		assert.Equal(t, "/team/beta", definitions[1].UserPath, "b after batch = %+v", definitions[1])
		assert.False(t, definitions[1].CreatedAt.IsZero(), "b after batch = %+v, want CreatedAt stamped", definitions[1])
	})
}

func TestStoreGetAndDeleteMissingReturnNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		_, err := store.Get(ctx, "absent")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Delete(ctx, "absent")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreDeleteRemovesDefinition(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, name := range []string{"g", "keep"} {
			err := store.Upsert(ctx, Definition{Name: name, Type: "system_prompt", Config: []byte(`{"content":"c"}`)})
			require.NoError(t, err, "Upsert(%s)", name)
		}
		// Names are trimmed on the way in, so a padded delete must still match.
		err := store.Delete(ctx, "  g  ")
		require.NoError(t, err)
		_, err = store.Get(ctx, "g")
		assert.ErrorIs(t, err, ErrNotFound, "Get after Delete")
		err = store.Delete(ctx, "g")
		assert.ErrorIs(t, err, ErrNotFound, "second Delete")

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		if assert.Len(t, definitions, 1, "List after Delete") {
			assert.Equal(t, "keep", definitions[0].Name)
		}
	})
}
