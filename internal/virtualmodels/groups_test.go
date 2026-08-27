package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestService_PolicyGroupsGateAccess(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	// Group memberships: /team/alpha (and descendants) carry "beta-testers".
	svc.SetGroupResolver(func(userPath string) []string {
		path, err := core.NormalizeUserPath(userPath)
		if err != nil {
			return nil
		}
		for _, ancestor := range core.UserPathAncestors(path) {
			if ancestor == "/team/alpha" {
				return []string{"beta-testers"}
			}
		}
		return nil
	})

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "openai/gpt-4o",
		Groups:  []string{"beta-testers"},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	// No user path -> no groups -> denied.
	if err := svc.ValidateModelAccess(ctx, selector); err == nil {
		t.Fatalf("ValidateModelAccess(no identity) error = nil, want denied")
	}
	// Member path (via descendant) -> allowed.
	memberCtx := core.WithEffectiveUserPath(ctx, "/team/alpha/service")
	if err := svc.ValidateModelAccess(memberCtx, selector); err != nil {
		t.Fatalf("ValidateModelAccess(member) error = %v, want allowed", err)
	}
	if !svc.AllowsModel(memberCtx, selector) {
		t.Fatalf("AllowsModel(member) = false, want true")
	}
	// Non-member path -> denied.
	otherCtx := core.WithEffectiveUserPath(ctx, "/team/beta")
	if err := svc.ValidateModelAccess(otherCtx, selector); err == nil {
		t.Fatalf("ValidateModelAccess(non-member) error = nil, want denied")
	}
}

func TestService_PolicyPathsOrGroups(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	svc.SetGroupResolver(func(userPath string) []string {
		if userPath == "/vip" {
			return []string{"premium"}
		}
		return nil
	})

	if err := svc.Upsert(ctx, VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team"},
		Groups:    []string{"premium"},
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	// Allowed via path.
	if err := svc.ValidateModelAccess(core.WithEffectiveUserPath(ctx, "/team/alice"), selector); err != nil {
		t.Fatalf("ValidateModelAccess(path) error = %v, want allowed", err)
	}
	// Allowed via group.
	if err := svc.ValidateModelAccess(core.WithEffectiveUserPath(ctx, "/vip"), selector); err != nil {
		t.Fatalf("ValidateModelAccess(group) error = %v, want allowed", err)
	}
	// Neither -> denied.
	if err := svc.ValidateModelAccess(core.WithEffectiveUserPath(ctx, "/other"), selector); err == nil {
		t.Fatalf("ValidateModelAccess(neither) error = nil, want denied")
	}
}

func TestService_GroupsWithoutResolverDenied(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "openai/gpt-4o",
		Groups:  []string{"premium"},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	if err := svc.ValidateModelAccess(core.WithEffectiveUserPath(ctx, "/vip"), selector); err == nil {
		t.Fatalf("ValidateModelAccess(no resolver) error = nil, want denied")
	}
}

func TestService_RedirectRejectsGroups(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Groups:  []string{"premium"},
		Enabled: true,
	})
	if !IsValidationError(err) {
		t.Fatalf("Upsert(redirect with groups) error = %v, want validation error", err)
	}
}

func TestService_GroupsRoundTripThroughStore(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "openai/gpt-4o",
		Groups:  []string{"z-group", "a-group", "a-group"},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	stored, ok := svc.Get("openai/gpt-4o")
	if !ok || stored == nil {
		t.Fatalf("Get() not found after refresh")
	}
	if len(stored.Groups) != 2 || stored.Groups[0] != "a-group" || stored.Groups[1] != "z-group" {
		t.Fatalf("Get().Groups = %v, want sorted deduped [a-group z-group]", stored.Groups)
	}
}
