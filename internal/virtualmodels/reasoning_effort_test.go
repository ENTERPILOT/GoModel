package virtualmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestResolveReasoningEffort(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	mini := core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"}
	modelPolicy := func(model, effort string, userPaths ...string) VirtualModel {
		return VirtualModel{
			Source: "openai/" + model, ProviderName: "openai", Model: model,
			UserPaths: userPaths, ReasoningEffort: effort, Enabled: true,
		}
	}
	wideModelPolicy := func(model, effort string) VirtualModel {
		return VirtualModel{Source: model, Model: model, ReasoningEffort: effort, Enabled: true}
	}
	balancer := VirtualModel{
		Source:          "team-model",
		Targets:         []Target{{Provider: "openai", Model: "gpt-4o"}, {Provider: "openai", Model: "gpt-4o-mini"}},
		ReasoningEffort: "high",
		Enabled:         true,
	}

	tests := []struct {
		name      string
		rows      []VirtualModel
		requested core.RequestedModelSelector
		attempt   core.ModelSelector
		ctx       context.Context
		want      string
	}{
		{
			name:      "no override keeps the client value",
			rows:      []VirtualModel{{Source: "plain", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}},
			requested: core.NewRequestedModelSelector("plain", ""),
			attempt:   gpt4o,
			want:      "",
		},
		{
			name:      "virtual model override",
			rows:      []VirtualModel{balancer},
			requested: core.NewRequestedModelSelector("team-model", ""),
			attempt:   mini,
			want:      "high",
		},
		{
			name:      "model override on a direct request",
			rows:      []VirtualModel{modelPolicy("gpt-4o", "low")},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			want:      "low",
		},
		{
			name:      "model override beats the virtual model above it",
			rows:      []VirtualModel{balancer, modelPolicy("gpt-4o-mini", "medium")},
			requested: core.NewRequestedModelSelector("team-model", ""),
			attempt:   mini,
			want:      "medium",
		},
		{
			name:      "other legs keep the virtual model override",
			rows:      []VirtualModel{balancer, modelPolicy("gpt-4o-mini", "medium")},
			requested: core.NewRequestedModelSelector("team-model", ""),
			attempt:   gpt4o,
			want:      "high",
		},
		{
			name: "inner virtual model beats the outer one",
			rows: []VirtualModel{
				{Source: "outer", Targets: []Target{{Model: "inner"}}, ReasoningEffort: "high", Enabled: true},
				{Source: "inner", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "low", Enabled: true},
			},
			requested: core.NewRequestedModelSelector("outer", ""),
			attempt:   gpt4o,
			want:      "low",
		},
		{
			name: "outer virtual model applies when the inner one sets none",
			rows: []VirtualModel{
				{Source: "outer", Targets: []Target{{Model: "inner"}, {Provider: "openai", Model: "gpt-4o-mini"}}, ReasoningEffort: "high", Enabled: true},
				{Source: "inner", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true},
			},
			requested: core.NewRequestedModelSelector("outer", ""),
			attempt:   gpt4o,
			want:      "high",
		},
		{
			name: "inner virtual model override stays on its own legs",
			rows: []VirtualModel{
				{Source: "outer", Targets: []Target{{Model: "inner"}, {Provider: "openai", Model: "gpt-4o-mini"}}, Enabled: true},
				{Source: "inner", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "low", Enabled: true},
			},
			requested: core.NewRequestedModelSelector("outer", ""),
			attempt:   mini,
			want:      "",
		},
		{
			name:      "explicit provider request skips the redirect",
			rows:      []VirtualModel{{Source: "gpt-4o", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "high", Enabled: true}},
			requested: core.NewRequestedModelSelector("gpt-4o", "openai"),
			attempt:   gpt4o,
			want:      "",
		},
		{
			name:      "model override for a matching user path",
			rows:      []VirtualModel{modelPolicy("gpt-4o", "low", "/team/alpha")},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/alpha/member"),
			want:      "low",
		},
		{
			name:      "provider-wide policy does not hide a model-wide override",
			rows:      []VirtualModel{wideModelPolicy("gpt-4o", "high"), {Source: "openai/", ProviderName: "openai", Enabled: true}},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			want:      "high",
		},
		{
			name:      "exact policy without an effort does not hide a model-wide override",
			rows:      []VirtualModel{wideModelPolicy("gpt-4o", "high"), modelPolicy("gpt-4o", "", "/team/alpha")},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			want:      "high",
		},
		{
			name:      "exact override beats a model-wide one",
			rows:      []VirtualModel{wideModelPolicy("gpt-4o", "high"), modelPolicy("gpt-4o", "low")},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			want:      "low",
		},
		{
			name:      "exact override for another user path falls back to the model-wide one",
			rows:      []VirtualModel{wideModelPolicy("gpt-4o", "high"), modelPolicy("gpt-4o", "low", "/team/alpha")},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/beta"),
			want:      "high",
		},
		{
			name:      "model override skipped for another user path",
			rows:      []VirtualModel{modelPolicy("gpt-4o", "low", "/team/alpha")},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			attempt:   gpt4o,
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/beta"),
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			service := newSlowdownService(t, tt.rows...)
			assert.Equal(t, tt.want, service.ResolveReasoningEffort(ctx, &core.RequestModelResolution{Requested: tt.requested}, tt.attempt))
		})
	}
}

func TestUpsertValidatesReasoningEffort(t *testing.T) {
	tests := []struct {
		name        string
		vm          VirtualModel
		want        string
		wantInvalid bool
	}{
		{name: "omitted on a model", vm: VirtualModel{Source: "openai/gpt-4o"}},
		{name: "model", vm: VirtualModel{Source: "openai/gpt-4o", ReasoningEffort: "medium"}, want: "medium"},
		{name: "normalized", vm: VirtualModel{Source: "openai/gpt-4o", ReasoningEffort: " XHigh "}, want: "xhigh"},
		{name: "redirect", vm: VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "none"}, want: "none"},
		{name: "unknown level", vm: VirtualModel{Source: "openai/gpt-4o", ReasoningEffort: "extreme"}, wantInvalid: true},
		{name: "unknown level on a redirect", vm: VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "extreme"}, wantInvalid: true},
		{name: "provider scope", vm: VirtualModel{Source: "openai/", ReasoningEffort: "low"}, wantInvalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSlowdownService(t)
			tt.vm.Enabled = true
			err := service.Upsert(context.Background(), tt.vm)
			if tt.wantInvalid {
				require.Error(t, err)
				assert.True(t, IsValidationError(err))
				return
			}
			require.NoError(t, err)
			got, ok := service.Get(tt.vm.Source)
			if tt.want == "" && !ok {
				return // an empty policy is a no-op and is not stored
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, got.ReasoningEffort)
		})
	}
}

// Two legs of a load balancer reach the same concrete model with different
// efforts: each primary attempt gets the effort of the leg routing chose.
func TestResolveReasoningEffortFollowsRoutedPath(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	service := newSlowdownService(t,
		VirtualModel{Source: "team", Targets: []Target{{Model: "fast"}, {Model: "smart"}}, Enabled: true},
		VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "low", Enabled: true},
		VirtualModel{Source: "smart", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, ReasoningEffort: "high", Enabled: true},
	)
	requested := core.NewRequestedModelSelector("team", "")

	seen := map[string]bool{}
	for range 2 {
		resolved, path, changed, err := service.ResolveModelRouteForUserPath(context.Background(), requested)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, gpt4o, resolved)
		require.Len(t, path, 2)
		assert.Equal(t, "team", path[0])

		resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: resolved, VirtualModelPath: path}
		want := map[string]string{"fast": "low", "smart": "high"}[path[1]]
		assert.Equal(t, want, service.ResolveReasoningEffort(context.Background(), resolution, gpt4o), "route %v", path)
		seen[path[1]] = true
	}
	assert.Len(t, seen, 2, "round robin should route through both legs")
}

// Without a redirect forcing an effort, routing records no route.
func TestResolveModelRouteOmitsRouteWithoutEffort(t *testing.T) {
	service := newSlowdownService(t,
		VirtualModel{Source: "team", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true},
		VirtualModel{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", ReasoningEffort: "low", Enabled: true},
	)

	resolved, path, changed, err := service.ResolveModelRouteForUserPath(context.Background(), core.NewRequestedModelSelector("team", ""))
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, core.ModelSelector{Provider: "openai", Model: "gpt-4o"}, resolved)
	assert.Nil(t, path)
}
