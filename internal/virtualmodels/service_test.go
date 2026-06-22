package virtualmodels

import (
	"context"
	"testing"

	"gomodel/internal/core"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(newSQLiteVMStore(t), testCatalog(), true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func TestService_RedirectResolves(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	sel, changed, err := svc.Aliases().ResolveModel(core.NewRequestedModelSelector("fast", ""))
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if !changed {
		t.Fatalf("ResolveModel() changed = false, want true")
	}
	if sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("ResolveModel() = %q, want openai/gpt-4o", sel.QualifiedModel())
	}
}

func TestService_PolicyGatesAccess(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team"},
	}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	// No user path on the request -> access denied.
	if err := svc.Overrides().ValidateModelAccess(ctx, selector); err == nil {
		t.Fatalf("ValidateModelAccess(no path) error = nil, want denied")
	}

	// Matching ancestor user path -> allowed.
	allowedCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	if err := svc.Overrides().ValidateModelAccess(allowedCtx, selector); err != nil {
		t.Fatalf("ValidateModelAccess(/team/alice) error = %v, want allowed", err)
	}
}

func TestService_RejectsCrossKindClobber(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "gpt-fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}
	// A policy with the same source must be rejected, not silently clobber it.
	err := svc.Upsert(ctx, VirtualModel{Source: "gpt-fast", UserPaths: []string{"/team"}})
	if err == nil {
		t.Fatalf("Upsert(policy over redirect) error = nil, want rejection")
	}

	// The redirect must survive intact.
	got, getErr := svc.store.Get(ctx, "gpt-fast")
	if getErr != nil {
		t.Fatalf("store.Get() error = %v", getErr)
	}
	if !got.IsRedirect() {
		t.Fatalf("redirect was clobbered: %#v", got)
	}
}

func TestService_RejectsMultiTargetRedirect(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}, {Provider: "azure", Model: "gpt-4o"}},
	})
	if err == nil {
		t.Fatalf("Upsert(multi-target) error = nil, want rejection")
	}
}

func TestService_ListViewsAndDeleteRoute(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	views := svc.ListViews()
	if len(views) != 2 {
		t.Fatalf("len(ListViews()) = %d, want 2", len(views))
	}
	kinds := map[string]string{}
	for _, v := range views {
		kinds[v.Source] = v.Kind
	}
	if kinds["fast"] != KindRedirect {
		t.Fatalf("views[fast].Kind = %q, want %q", kinds["fast"], KindRedirect)
	}
	if kinds["openai/gpt-4o"] != KindPolicy {
		t.Fatalf("views[openai/gpt-4o].Kind = %q, want %q", kinds["openai/gpt-4o"], KindPolicy)
	}

	// Delete routes to the right engine based on stored kind.
	if err := svc.Delete(ctx, "fast"); err != nil {
		t.Fatalf("Delete(fast) error = %v", err)
	}
	if err := svc.Delete(ctx, "openai/gpt-4o"); err != nil {
		t.Fatalf("Delete(openai/gpt-4o) error = %v", err)
	}
	if views := svc.ListViews(); len(views) != 0 {
		t.Fatalf("len(ListViews()) after delete = %d, want 0", len(views))
	}
}
