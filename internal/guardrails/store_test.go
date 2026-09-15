package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

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
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

// assertJSONEqual compares configs semantically: PostgreSQL's JSONB and the
// MongoDB document store both reorder keys and drop whitespace.
func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("config %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want %q is not JSON: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("config = %s, want %s", got, want)
	}
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
			if err := store.Upsert(ctx, definition); err != nil {
				t.Fatalf("Upsert(%s): %v", definition.Name, err)
			}
		}

		got, err := store.Get(ctx, "scoped")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "scoped" || got.Type != "system_prompt" || got.Description != "team prompt" || got.UserPath != "/team/alpha" {
			t.Errorf("identity = %q/%q/%q/%q", got.Name, got.Type, got.Description, got.UserPath)
		}
		if got.FailMode != "open" || got.TimeoutMS != 1500 {
			t.Errorf("fail_mode/timeout_ms = %q/%d, want open/1500", got.FailMode, got.TimeoutMS)
		}
		assertJSONEqual(t, got.Config, `{"content":"be concise","max_tokens":100,"tags":["a","b"]}`)
		// A caller-supplied created_at is kept; updated_at is always stamped
		// by the store. Both come back in UTC.
		if !got.CreatedAt.Equal(created) {
			t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
		}
		if got.UpdatedAt.Before(start) {
			t.Errorf("UpdatedAt = %v, want >= %v", got.UpdatedAt, start)
		}
		if got.CreatedAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC {
			t.Errorf("timestamps not UTC: %v / %v", got.CreatedAt.Location(), got.UpdatedAt.Location())
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 2 {
			t.Fatalf("len = %d, want 2", len(definitions))
		}
		// Ordered by name ascending.
		if definitions[0].Name != "global" || definitions[1].Name != "scoped" {
			t.Fatalf("names = %s, %s; want global, scoped", definitions[0].Name, definitions[1].Name)
		}
		if definitions[0].UserPath != "" || definitions[0].Description != "" || definitions[0].FailMode != "" || definitions[0].TimeoutMS != 0 {
			t.Errorf("global row carries non-default optional fields: %+v", definitions[0])
		}
		if definitions[0].CreatedAt.IsZero() || definitions[0].CreatedAt.Before(start) {
			t.Errorf("global CreatedAt = %v, want stamped >= %v", definitions[0].CreatedAt, start)
		}
		if !reflect.DeepEqual(definitions[1], *got) {
			t.Errorf("List row = %+v, want Get result %+v", definitions[1], *got)
		}
	})
}

func TestStoreListOnEmptyStore(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		definitions, err := store.List(context.Background())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if definitions == nil || len(definitions) != 0 {
			t.Fatalf("List = %#v, want empty non-nil slice", definitions)
		}
	})
}

func TestStoreListOrdersByName(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, name := range []string{"charlie", "alpha", "bravo"} {
			if err := store.Upsert(ctx, Definition{Name: name, Type: "system_prompt"}); err != nil {
				t.Fatalf("Upsert(%s): %v", name, err)
			}
		}
		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		names := make([]string, 0, len(definitions))
		for _, definition := range definitions {
			names = append(names, definition.Name)
		}
		if want := []string{"alpha", "bravo", "charlie"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("names = %v, want %v", names, want)
		}
	})
}

func TestStoreUpsertNormalizesIdentity(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		if err := store.Upsert(ctx, Definition{
			Name:        "  padded  ",
			Type:        " System-Prompt ",
			Description: "  desc  ",
			UserPath:    "team/alpha/",
			FailMode:    " Fail-Open ",
			Config:      []byte(`{"content":"c"}`),
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := store.Get(ctx, "padded")
		if err != nil {
			t.Fatalf("Get(padded): %v", err)
		}
		if got.Name != "padded" || got.Type != "system_prompt" || got.Description != "desc" || got.UserPath != "/team/alpha" || got.FailMode != "open" {
			t.Fatalf("normalized = %+v", got)
		}
		// Get trims the lookup name too.
		if _, err := store.Get(ctx, "  padded "); err != nil {
			t.Fatalf("Get(padded name): %v", err)
		}
	})
}

// TestStoreEmptyConfigNormalizesToEmptyObject pins that a definition saved
// without a config reads back as {} rather than an empty string.
func TestStoreEmptyConfigNormalizesToEmptyObject(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, config := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("   ")} {
			if err := store.Upsert(ctx, Definition{Name: "empty", Type: "system_prompt", Config: config}); err != nil {
				t.Fatalf("Upsert(%q): %v", config, err)
			}
			got, err := store.Get(ctx, "empty")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
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
		if err := store.Upsert(ctx, Definition{Name: "null", Type: "system_prompt", Config: json.RawMessage("null")}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := store.Get(ctx, "null")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertJSONEqual(t, got.Config, `null`)
	})
}

func TestStoreUpsertOverwritesAndPreservesCreatedAt(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Upsert(ctx, Definition{
			Name: "g", Type: "system_prompt", Description: "first", UserPath: "/team/alpha",
			FailMode: "open", TimeoutMS: 1500, Config: []byte(`{"content":"c"}`),
		}); err != nil {
			t.Fatalf("first Upsert: %v", err)
		}
		created, err := store.Get(ctx, "g")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}

		// A re-upsert replaces every field but created_at; optional fields
		// left empty fall back to their defaults rather than lingering.
		if err := store.Upsert(ctx, Definition{Name: "g", Type: "llm_based_altering", Config: []byte(`{"model":"openai/gpt-4o"}`)}); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}
		updated, err := store.Get(ctx, "g")
		if err != nil {
			t.Fatalf("Get after update: %v", err)
		}
		if updated.Type != "llm_based_altering" {
			t.Errorf("Type = %q, want llm_based_altering", updated.Type)
		}
		assertJSONEqual(t, updated.Config, `{"model":"openai/gpt-4o"}`)
		if updated.Description != "" || updated.UserPath != "" || updated.FailMode != "" || updated.TimeoutMS != 0 {
			t.Errorf("optional fields not reset: %+v", updated)
		}
		if !updated.CreatedAt.Equal(created.CreatedAt) {
			t.Errorf("CreatedAt = %v, want %v preserved", updated.CreatedAt, created.CreatedAt)
		}
		if updated.UpdatedAt.Before(created.UpdatedAt) {
			t.Errorf("UpdatedAt = %v, want >= %v", updated.UpdatedAt, created.UpdatedAt)
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 1 {
			t.Errorf("len = %d after re-upsert, want 1", len(definitions))
		}
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
			if !IsValidationError(err) {
				t.Errorf("%s: Upsert error = %v, want validation error", name, err)
			}
		}
		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 0 {
			t.Errorf("len = %d after rejected upserts, want 0", len(definitions))
		}
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
		if !IsValidationError(err) {
			t.Fatalf("UpsertMany with an invalid definition error = %v, want validation error", err)
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 0 {
			t.Errorf("len = %d after failed batch, want 0", len(definitions))
		}
	})
}

func TestStoreUpsertManyCommits(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.UpsertMany(ctx, nil); err != nil {
			t.Fatalf("UpsertMany(nil): %v", err)
		}
		if err := store.Upsert(ctx, Definition{Name: "a", Type: "system_prompt", Description: "old", Config: []byte(`{"content":"old"}`)}); err != nil {
			t.Fatalf("Upsert(a): %v", err)
		}
		existing, err := store.Get(ctx, "a")
		if err != nil {
			t.Fatalf("Get(a): %v", err)
		}

		if err := store.UpsertMany(ctx, []Definition{
			{Name: "a", Type: "system_prompt", Config: []byte(`{"content":"new"}`)},
			{Name: "b", Type: "system_prompt", UserPath: "/team/beta", Config: []byte(`{"content":"c"}`)},
		}); err != nil {
			t.Fatalf("UpsertMany: %v", err)
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 2 || definitions[0].Name != "a" || definitions[1].Name != "b" {
			t.Fatalf("List = %+v, want a then b", definitions)
		}
		// The batch overwrites like Upsert does: fields replaced, created_at kept.
		assertJSONEqual(t, definitions[0].Config, `{"content":"new"}`)
		if definitions[0].Description != "" || !definitions[0].CreatedAt.Equal(existing.CreatedAt) {
			t.Errorf("a after batch = %+v, want description reset and CreatedAt %v", definitions[0], existing.CreatedAt)
		}
		if definitions[1].UserPath != "/team/beta" || definitions[1].CreatedAt.IsZero() {
			t.Errorf("b after batch = %+v", definitions[1])
		}
	})
}

func TestStoreGetAndDeleteMissingReturnNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		if _, err := store.Get(ctx, "absent"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get error = %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, "absent"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete error = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreDeleteRemovesDefinition(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, name := range []string{"g", "keep"} {
			if err := store.Upsert(ctx, Definition{Name: name, Type: "system_prompt", Config: []byte(`{"content":"c"}`)}); err != nil {
				t.Fatalf("Upsert(%s): %v", name, err)
			}
		}
		// Names are trimmed on the way in, so a padded delete must still match.
		if err := store.Delete(ctx, "  g  "); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Get(ctx, "g"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after Delete = %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, "g"); !errors.Is(err, ErrNotFound) {
			t.Errorf("second Delete = %v, want ErrNotFound", err)
		}
		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 1 || definitions[0].Name != "keep" {
			t.Errorf("List after Delete = %+v, want only keep", definitions)
		}
	})
}
