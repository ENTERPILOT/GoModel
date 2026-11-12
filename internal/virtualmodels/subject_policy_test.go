package virtualmodels

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// pathPolicyStub is a subject-side policy that allows every model and sets a
// request policy on one user path.
type pathPolicyStub struct {
	userPath     string
	ceilings     requestparams.Ceilings
	defaultModel string
}

func (p pathPolicyStub) AllowsModel(context.Context, core.ModelSelector) bool { return true }

func (p pathPolicyStub) RequestCeilings(ctx context.Context) requestparams.Ceilings {
	if core.UserPathFromContext(ctx) != p.userPath {
		return requestparams.Ceilings{}
	}
	return p.ceilings
}

func (p pathPolicyStub) DefaultModel(ctx context.Context) string {
	if core.UserPathFromContext(ctx) != p.userPath {
		return ""
	}
	return p.defaultModel
}

// The caller's ceilings ride on every attempt next to whatever the virtual
// models on its route force: the primary, a failover leg, a direct request
// to a concrete model, and a request no virtual model touches.
func TestResolveRequestOverridesAddsSubjectCeilings(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	mini := core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"}
	ceilings := requestparams.Ceilings{ReasoningEffort: "medium", OutputTokens: 4096}
	balancer := VirtualModel{
		Source:          "team-model",
		Targets:         []Target{{Provider: "openai", Model: "gpt-4o"}, {Provider: "openai", Model: "gpt-4o-mini"}},
		ReasoningEffort: "high",
		DefaultParams:   requestparams.Set{"max_tokens": json.RawMessage(`8000`)},
		Enabled:         true,
	}
	miniPolicy := VirtualModel{Source: "openai/gpt-4o-mini", ProviderName: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", Enabled: true}
	eng := core.WithEffectiveUserPath(context.Background(), "/eng")
	other := core.WithEffectiveUserPath(context.Background(), "/sales")

	tests := []struct {
		name         string
		rows         []VirtualModel
		requested    core.RequestedModelSelector
		attempt      core.ModelSelector
		ctx          context.Context
		wantEffort   string
		wantCeilings requestparams.Ceilings
	}{
		{
			name:         "no virtual model overrides at all",
			requested:    core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:      gpt4o,
			ctx:          eng,
			wantCeilings: ceilings,
		},
		{
			name:         "primary attempt through a redirect",
			rows:         []VirtualModel{balancer, miniPolicy},
			requested:    core.NewRequestedModelSelector("team-model", ""),
			attempt:      gpt4o,
			ctx:          eng,
			wantEffort:   "high",
			wantCeilings: ceilings,
		},
		{
			name:         "failover leg with its own model policy",
			rows:         []VirtualModel{balancer, miniPolicy},
			requested:    core.NewRequestedModelSelector("team-model", ""),
			attempt:      mini,
			ctx:          eng,
			wantEffort:   "low",
			wantCeilings: ceilings,
		},
		{
			name:         "explicit provider skips redirects, not the ceilings",
			rows:         []VirtualModel{balancer},
			requested:    core.NewRequestedModelSelector("gpt-4o", "openai"),
			attempt:      gpt4o,
			ctx:          eng,
			wantCeilings: ceilings,
		},
		{
			name:       "another user path keeps the virtual model overrides only",
			rows:       []VirtualModel{balancer},
			requested:  core.NewRequestedModelSelector("team-model", ""),
			attempt:    gpt4o,
			ctx:        other,
			wantEffort: "high",
		},
		{
			name:      "no user path and nothing configured is a zero value",
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			ctx:       context.Background(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSlowdownService(t, tt.rows...)
			service.SetAccessPolicy(pathPolicyStub{userPath: "/eng", ceilings: ceilings})

			got := service.ResolveRequestOverrides(tt.ctx, &core.RequestModelResolution{Requested: tt.requested}, tt.attempt)

			assert.Equal(t, tt.wantEffort, got.ReasoningEffort)
			assert.Equal(t, tt.wantCeilings, got.Ceilings)
			assert.Equal(t, tt.wantEffort == "" && tt.wantCeilings.IsZero(), got.IsZero())
		})
	}
}

func TestServiceDefaultModelComesFromTheSubjectPolicy(t *testing.T) {
	service := newSlowdownService(t)
	eng := core.WithEffectiveUserPath(context.Background(), "/eng")
	assert.Empty(t, service.DefaultModel(eng), "no subject policy, no default")

	service.SetAccessPolicy(&denyAllPolicy{})
	assert.Empty(t, service.DefaultModel(eng), "an access policy without request policy sets no default")
	require.True(t, service.ResolveRequestOverrides(eng, &core.RequestModelResolution{}, core.ModelSelector{Provider: "openai", Model: "gpt-4o"}).IsZero())

	service.SetAccessPolicy(pathPolicyStub{userPath: "/eng", defaultModel: "team-model"})
	assert.Equal(t, "team-model", service.DefaultModel(eng))
	assert.Empty(t, service.DefaultModel(context.Background()))

	var absent *Service
	assert.Empty(t, absent.DefaultModel(eng))
}
