package users

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	svc, err := NewService(store, testCatalog{"openai", "anthropic"})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
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

	if !svc.AllowsModel(context.Background(), gpt) {
		t.Fatal("AllowsModel(no key, no path) = false, want true")
	}
	if !svc.AllowsModel(requestCtx("/acme/eng/alice"), gpt) {
		t.Fatal("AllowsModel(path without policies) = false, want true")
	}
}

func TestService_PathAllowlistsIntersectDownTheChain(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Upsert(ctx, User{UserPath: "acme", AllowedModels: []string{"openai/*", "anthropic/*"}}); err != nil {
		t.Fatalf("Upsert(/acme): %v", err)
	}
	if _, err := svc.Upsert(ctx, User{UserPath: "/acme/eng", AllowedModels: []string{"anthropic/*"}}); err != nil {
		t.Fatalf("Upsert(/acme/eng): %v", err)
	}
	// A child that tries to widen its group's restriction still gets the
	// intersection: the group's allowlist must match too.
	if _, err := svc.Upsert(ctx, User{UserPath: "/acme/eng/bob", AllowedModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-6"}}); err != nil {
		t.Fatalf("Upsert(/acme/eng/bob): %v", err)
	}

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
			if got := svc.AllowsModel(tc.ctx, tc.selector); got != tc.want {
				t.Fatalf("AllowsModel(%s) = %v, want %v", tc.selector.QualifiedModel(), got, tc.want)
			}
		})
	}

	constraints := svc.Constraints("/acme/eng/bob/x")
	paths := make([]string, 0, len(constraints))
	for _, c := range constraints {
		paths = append(paths, c.UserPath)
	}
	if want := []string{"/acme", "/acme/eng", "/acme/eng/bob"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("Constraints = %v, want %v", paths, want)
	}
}

func TestService_UpsertValidatesAndDeleteRemoves(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Upsert(ctx, User{UserPath: "", AllowedModels: []string{"openai/*"}}); !IsValidationError(err) {
		t.Fatalf("Upsert(empty path) error = %v, want validation error", err)
	}
	if _, err := svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"nope/*"}}); !IsValidationError(err) {
		t.Fatalf("Upsert(unknown provider) error = %v, want validation error", err)
	}

	stored, err := svc.Upsert(ctx, User{UserPath: "acme/eng/", AllowedModels: []string{"anthropic/*", " "}, Description: " eng "})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if stored.UserPath != "/acme/eng" || stored.Description != "eng" || !reflect.DeepEqual(stored.AllowedModels, []string{"anthropic/"}) {
		t.Fatalf("stored = %#v", stored)
	}
	if got := svc.List(); len(got) != 1 || got[0].UserPath != "/acme/eng" {
		t.Fatalf("List = %#v, want one /acme/eng row", got)
	}

	if err := svc.Delete(ctx, "/acme/eng"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, "/acme/eng"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}
	if got := svc.List(); len(got) != 0 {
		t.Fatalf("List after delete = %#v, want empty", got)
	}
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
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	store := &flakyStore{Store: inner}
	svc, err := NewService(store, testCatalog{"openai", "anthropic"})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"openai/*"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	gpt := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	claude := core.ModelSelector{Provider: "anthropic", Model: "claude-sonnet-4-6"}

	store.failList = true
	if _, err := svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"anthropic/*"}}); err != nil {
		t.Fatalf("Upsert with failing refresh: %v", err)
	}
	if svc.AllowsModel(requestCtx("/acme"), gpt) || !svc.AllowsModel(requestCtx("/acme"), claude) {
		t.Fatal("restrictive upsert not enforced after failed refresh")
	}
	if err := svc.Delete(ctx, "/acme"); err != nil {
		t.Fatalf("Delete with failing refresh: %v", err)
	}
	if !svc.AllowsModel(requestCtx("/acme"), gpt) {
		t.Fatal("deleted restriction still enforced after failed refresh")
	}
	if got := svc.List(); len(got) != 0 {
		t.Fatalf("List after delete = %#v, want empty", got)
	}
}

func TestService_ConfigUsersShadowStoreAndAreReadOnly(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"openai/*"}, Description: "stored"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	svc.SetConfigUsers([]User{{UserPath: "acme", AllowedModels: []string{"anthropic/*"}, Description: "declared"}})
	if err := svc.ValidateManagedConfig([]string{"anthropic"}); err != nil {
		t.Fatalf("ValidateManagedConfig: %v", err)
	}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	got, ok := svc.Get("/acme")
	if !ok || !got.Managed || got.Description != "declared" || !reflect.DeepEqual(got.AllowedModels, []string{"anthropic/"}) {
		t.Fatalf("Get(/acme) = %#v, want managed declared row", got)
	}
	if _, err := svc.Upsert(ctx, User{UserPath: "/acme"}); !errors.Is(err, ErrManaged) {
		t.Fatalf("Upsert(managed) error = %v, want ErrManaged", err)
	}
	if err := svc.Delete(ctx, "/acme"); !errors.Is(err, ErrManaged) {
		t.Fatalf("Delete(managed) error = %v, want ErrManaged", err)
	}

	svc.SetConfigUsers([]User{{UserPath: "/acme", AllowedModels: []string{"missing/*"}}})
	if err := svc.ValidateManagedConfig([]string{"anthropic"}); err == nil {
		t.Fatal("ValidateManagedConfig(unknown provider) error = nil, want error")
	}
}
