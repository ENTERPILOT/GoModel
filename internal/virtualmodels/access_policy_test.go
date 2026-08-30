package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

type denyAllPolicy struct{ calls int }

func (p *denyAllPolicy) AllowsModel(context.Context, core.ModelSelector) bool {
	p.calls++
	return false
}

func TestService_AccessPolicyNarrowsAfterModelSideRows(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	// Without a policy the model-side rows alone decide.
	if !svc.AllowsModel(ctx, selector) {
		t.Fatal("AllowsModel() = false before installing a policy, want true")
	}

	policy := &denyAllPolicy{}
	svc.SetAccessPolicy(policy)

	if svc.AllowsModel(ctx, selector) {
		t.Fatal("AllowsModel() = true, want false from the subject-side policy")
	}
	err := svc.ValidateModelAccess(ctx, selector)
	if err == nil {
		t.Fatal("ValidateModelAccess() error = nil, want model_access_denied")
	}
	if gatewayErr, ok := err.(*core.GatewayError); !ok || gatewayErr.Code == nil || *gatewayErr.Code != "model_access_denied" {
		t.Fatalf("ValidateModelAccess() error = %v, want model_access_denied", err)
	}
	if policy.calls != 2 {
		t.Fatalf("policy consulted %d times, want 2", policy.calls)
	}

	// A model the model-side rows already deny never reaches the policy.
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}
	if svc.AllowsModel(ctx, selector) {
		t.Fatal("AllowsModel() = true, want false from the model-side row")
	}
	if policy.calls != 2 {
		t.Fatalf("policy consulted %d times after model-side denial, want 2", policy.calls)
	}

	models := svc.FilterPublicModels(core.WithEffectiveUserPath(ctx, "/team"), []core.Model{{ID: "openai/gpt-4o"}})
	if len(models) != 0 {
		t.Fatalf("FilterPublicModels() = %v, want empty", models)
	}
}
