package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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
	require.True(t, svc.AllowsModel(ctx, selector))

	policy := &denyAllPolicy{}
	svc.SetAccessPolicy(policy)

	require.False(t, svc.AllowsModel(ctx, selector))

	err := svc.ValidateModelAccess(ctx, selector)
	require.Error(t, err)
	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.NotNil(t, gatewayErr.Code)
	require.Equal(t, "model_access_denied", *gatewayErr.Code)
	require.Equal(t, 2, policy.calls)
	// A model the model-side rows already deny never reaches the policy.
	err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}, Enabled: true})
	require.NoError(t, err)
	require.False(t, svc.AllowsModel(ctx, selector))
	require.Equal(t, 2, policy.calls)

	models := svc.FilterPublicModels(core.WithEffectiveUserPath(ctx, "/team"), []core.Model{{ID: "openai/gpt-4o"}})
	require.Empty(t, models)
}
