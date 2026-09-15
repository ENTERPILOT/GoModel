package tagging

import (
	"context"
	"testing"
)

func TestStore_RoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		want := []Rule{
			{Header: "X-Team", Prefix: "team-", Delimiter: ","},
			{Header: "X-Env", DoNotPass: true, Delimiter: "|"},
			{Header: "X-Bare"},
		}
		if err := store.SaveRules(ctx, want); err != nil {
			t.Fatalf("SaveRules: %v", err)
		}

		got, err := store.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d rules, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("rule %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

func TestStore_ManagedFlagIsNeverPersisted(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		// Managed marks declarative config/env rules; a caller that saves one
		// by mistake must not be able to make it read-only in the dashboard.
		if err := store.SaveRules(ctx, []Rule{{Header: "X-Team", Managed: true}}); err != nil {
			t.Fatalf("SaveRules: %v", err)
		}
		got, err := store.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 1 || got[0].Managed {
			t.Errorf("got %+v, want X-Team with Managed=false", got)
		}
	})
}

func TestStore_GetRulesEmptyWhenUnset(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		// A store with nothing saved must read as "no operator rules", not as
		// an error: it is the state of every fresh deployment.
		got, err := store.GetRules(context.Background())
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d rules, want none", len(got))
		}
	})
}

func TestStore_SaveReplacesPreviousRules(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.SaveRules(ctx, []Rule{{Header: "X-One"}, {Header: "X-Two"}}); err != nil {
			t.Fatalf("first SaveRules: %v", err)
		}
		// SaveRules replaces the whole set rather than merging, so a shorter
		// second save must not leave the dropped rule behind.
		if err := store.SaveRules(ctx, []Rule{{Header: "X-Three"}}); err != nil {
			t.Fatalf("second SaveRules: %v", err)
		}

		got, err := store.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 1 || got[0].Header != "X-Three" {
			t.Errorf("got %+v, want only X-Three", got)
		}
	})
}

func TestStore_SaveEmptyClearsRules(t *testing.T) {
	for name, empty := range map[string][]Rule{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			runStoreSuite(t, func(t *testing.T, store Store) {
				ctx := context.Background()

				if err := store.SaveRules(ctx, []Rule{{Header: "X-One"}}); err != nil {
					t.Fatalf("SaveRules: %v", err)
				}
				if err := store.SaveRules(ctx, empty); err != nil {
					t.Fatalf("SaveRules(%s): %v", name, err)
				}

				got, err := store.GetRules(ctx)
				if err != nil {
					t.Fatalf("GetRules: %v", err)
				}
				if len(got) != 0 {
					t.Errorf("got %+v, want none", got)
				}
			})
		})
	}
}
