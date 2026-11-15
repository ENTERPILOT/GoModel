package virtualmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestResolveTargetReasoningEffort(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	mini := core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"}
	team := func(effort string, targets ...Target) VirtualModel {
		return VirtualModel{Source: "team", Targets: targets, ReasoningEffort: effort, Enabled: true}
	}
	inner := func(effort string, targets ...Target) VirtualModel {
		return VirtualModel{Source: "inner", Targets: targets, ReasoningEffort: effort, Enabled: true}
	}
	gpt4oTarget := Target{Provider: "openai", Model: "gpt-4o", ReasoningEffort: "high"}
	miniTarget := Target{Provider: "openai", Model: "gpt-4o-mini"}

	tests := []struct {
		name    string
		rows    []VirtualModel
		attempt core.ModelSelector
		want    string
	}{
		{
			name:    "target effort applies to its own leg",
			rows:    []VirtualModel{team("", gpt4oTarget, miniTarget)},
			attempt: gpt4o,
			want:    "high",
		},
		{
			name:    "other legs keep the client value",
			rows:    []VirtualModel{team("", gpt4oTarget, miniTarget)},
			attempt: mini,
			want:    "",
		},
		{
			name:    "target effort beats the virtual model's own",
			rows:    []VirtualModel{team("low", gpt4oTarget, miniTarget)},
			attempt: gpt4o,
			want:    "high",
		},
		{
			name:    "other legs keep the virtual model's own effort",
			rows:    []VirtualModel{team("low", gpt4oTarget, miniTarget)},
			attempt: mini,
			want:    "low",
		},
		{
			name: "model policy beats the target effort",
			rows: []VirtualModel{
				team("", gpt4oTarget),
				{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", ReasoningEffort: "medium", Enabled: true},
			},
			attempt: gpt4o,
			want:    "medium",
		},
		{
			name:    "target declared without a provider",
			rows:    []VirtualModel{team("", Target{Model: "gpt-4o", ReasoningEffort: "high"})},
			attempt: gpt4o,
			want:    "high",
		},
		{
			name: "effort on a chained target reaches the models behind it",
			rows: []VirtualModel{
				team("", Target{Model: "inner", ReasoningEffort: "high"}),
				inner("", Target{Provider: "openai", Model: "gpt-4o"}),
			},
			attempt: gpt4o,
			want:    "high",
		},
		{
			name: "chained virtual model's own effort beats the target effort above it",
			rows: []VirtualModel{
				team("", Target{Model: "inner", ReasoningEffort: "high"}),
				inner("low", Target{Provider: "openai", Model: "gpt-4o"}),
			},
			attempt: gpt4o,
			want:    "low",
		},
		{
			name: "innermost target effort wins",
			rows: []VirtualModel{
				team("", Target{Model: "inner", ReasoningEffort: "high"}),
				inner("low", Target{Provider: "openai", Model: "gpt-4o", ReasoningEffort: "minimal"}),
			},
			attempt: gpt4o,
			want:    "minimal",
		},
		{
			name: "effort on a chained target stays off its sibling legs",
			rows: []VirtualModel{
				team("", Target{Model: "inner", ReasoningEffort: "high"}, miniTarget),
				inner("", Target{Provider: "openai", Model: "gpt-4o"}),
			},
			attempt: mini,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSlowdownService(t, tt.rows...)
			resolution := &core.RequestModelResolution{Requested: core.NewRequestedModelSelector("team", "")}
			assert.Equal(t, tt.want, service.ResolveRequestOverrides(context.Background(), resolution, tt.attempt).ReasoningEffort)
		})
	}
}

// A failover virtual model with an effort per target: the first attempt and
// every failover leg carry the effort of the target they go to.
func TestTargetReasoningEffortPerFailoverAttempt(t *testing.T) {
	svc := newBalancingService(t)
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "faq-reply-bot",
		Strategy: StrategyFailover,
		Targets: []Target{
			{Model: "openai/gpt-4o", ReasoningEffort: "high"},
			{Model: "anthropic/claude", ReasoningEffort: "low"},
			{Model: "groq/llama"},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	ctx := context.Background()
	requested := core.NewRequestedModelSelector("faq-reply-bot", "")
	primary, path, applied, err := svc.ResolveModelRouteForUserPath(ctx, requested)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, "openai/gpt-4o", primary.QualifiedModel())
	require.Equal(t, []string{"faq-reply-bot"}, path, "a target effort records the route like a virtual model effort")

	resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: primary, AliasApplied: applied, VirtualModelPath: path}
	efforts := map[string]string{primary.QualifiedModel(): svc.ResolveRequestOverrides(ctx, resolution, primary).ReasoningEffort}
	for _, leg := range svc.ResolveFailovers(resolution, core.OperationChatCompletions) {
		efforts[leg.QualifiedModel()] = svc.ResolveRequestOverrides(ctx, resolution, leg).ReasoningEffort
	}
	assert.Equal(t, map[string]string{"openai/gpt-4o": "high", "anthropic/claude": "low", "groq/llama": ""}, efforts)
}

// Two chained targets reach the same concrete model with different efforts:
// each first attempt gets the effort of the target routing chose.
func TestTargetReasoningEffortFollowsRoutedPath(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	service := newSlowdownService(t,
		VirtualModel{Source: "team", Targets: []Target{{Model: "fast", ReasoningEffort: "low"}, {Model: "smart", ReasoningEffort: "high"}}, Enabled: true},
		VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true},
		VirtualModel{Source: "smart", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true},
	)
	requested := core.NewRequestedModelSelector("team", "")

	seen := map[string]string{}
	for range 2 {
		resolved, path, _, err := service.ResolveModelRouteForUserPath(context.Background(), requested)
		require.NoError(t, err)
		require.Equal(t, gpt4o, resolved)
		require.Len(t, path, 2)

		resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: resolved, VirtualModelPath: path}
		seen[path[1]] = service.ResolveRequestOverrides(context.Background(), resolution, gpt4o).ReasoningEffort
	}
	assert.Equal(t, map[string]string{"fast": "low", "smart": "high"}, seen)
}

func TestUpsertValidatesTargetReasoningEffort(t *testing.T) {
	tests := []struct {
		name        string
		effort      string
		want        string
		wantInvalid bool
	}{
		{name: "omitted"},
		{name: "level", effort: "medium", want: "medium"},
		{name: "normalized", effort: " XHigh ", want: "xhigh"},
		{name: "unknown level", effort: "extreme", wantInvalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSlowdownService(t)
			err := service.Upsert(context.Background(), VirtualModel{
				Source:  "fast",
				Targets: []Target{{Provider: "openai", Model: "gpt-4o", ReasoningEffort: tt.effort}},
				Enabled: true,
			})
			if tt.wantInvalid {
				require.Error(t, err)
				assert.True(t, IsValidationError(err))
				return
			}
			require.NoError(t, err)
			got, ok := service.Get("fast")
			require.True(t, ok)
			require.Len(t, got.Targets, 1)
			assert.Equal(t, tt.want, got.Targets[0].ReasoningEffort)
		})
	}
}
