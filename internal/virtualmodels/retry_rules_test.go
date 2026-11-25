package virtualmodels

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func retryRules(rules ...RetryRule) []RetryRule {
	return rules
}

func TestResolveRetryRules(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	exact := VirtualModel{
		Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", Enabled: true,
		RetryRules: retryRules(
			RetryRule{Status: 429, MaxRetries: new(2), Delay: time.Second},
			RetryRule{Status: 503, MaxRetries: new(1), Delay: 250 * time.Millisecond},
			RetryRule{Status: 500, MaxRetries: new(0)},
		),
	}
	alias := func(source string, rules []RetryRule) VirtualModel {
		return VirtualModel{Source: source, Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, RetryRules: rules, Enabled: true}
	}

	tests := []struct {
		name      string
		rows      []VirtualModel
		requested core.RequestedModelSelector
		route     []string
		ctx       context.Context
		want      core.UpstreamRetryRules
	}{
		{
			name:      "nothing configured inherits",
			rows:      []VirtualModel{alias("plain", nil)},
			requested: core.NewRequestedModelSelector("plain", ""),
			want:      nil,
		},
		{
			name:      "alias rules beat model policy",
			rows:      []VirtualModel{alias("extract", retryRules(RetryRule{Status: 429, MaxRetries: new(3), Delay: 2 * time.Second})), exact},
			requested: core.NewRequestedModelSelector("extract", ""),
			route:     []string{"extract"},
			want: core.UpstreamRetryRules{
				{Status: 429, MaxRetries: 3, Delay: 2 * time.Second},
				{Status: 503, MaxRetries: 1, Delay: 250 * time.Millisecond},
				{Status: 500, MaxRetries: 0},
			},
		},
		{
			name:      "alias inherits model policy when omitted",
			rows:      []VirtualModel{alias("plain", nil), exact},
			requested: core.NewRequestedModelSelector("plain", ""),
			route:     []string{"plain"},
			want: core.UpstreamRetryRules{
				{Status: 429, MaxRetries: 2, Delay: time.Second},
				{Status: 503, MaxRetries: 1, Delay: 250 * time.Millisecond},
				{Status: 500, MaxRetries: 0},
			},
		},
		{
			name: "a redirect routing did not apply sets nothing",
			rows: []VirtualModel{
				alias("extract", retryRules(RetryRule{Status: 429, MaxRetries: new(3)})),
				exact,
			},
			requested: core.NewRequestedModelSelector("extract", ""),
			want: core.UpstreamRetryRules{
				{Status: 429, MaxRetries: 2, Delay: time.Second},
				{Status: 503, MaxRetries: 1, Delay: 250 * time.Millisecond},
				{Status: 500, MaxRetries: 0},
			},
		},
		{
			name: "outer redirect on the route wins over inner",
			rows: []VirtualModel{
				{Source: "outer", Targets: []Target{{Model: "inner"}}, RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(5)}), Enabled: true},
				alias("inner", retryRules(RetryRule{Status: 429, MaxRetries: new(3)})),
			},
			requested: core.NewRequestedModelSelector("outer", ""),
			route:     []string{"outer", "inner"},
			want:      core.UpstreamRetryRules{{Status: 429, MaxRetries: 5}},
		},
		{
			name: "policy for another user path is skipped",
			rows: []VirtualModel{
				{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/team/alpha"}, RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(2)}), Enabled: true},
			},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/beta"),
			want:      nil,
		},
		{
			name:      "explicit provider skips redirects",
			rows:      []VirtualModel{alias("gpt-4o-fast", retryRules(RetryRule{Status: 429, MaxRetries: new(3)})), exact},
			requested: core.NewRequestedModelSelector("gpt-4o", "openai"),
			want: core.UpstreamRetryRules{
				{Status: 429, MaxRetries: 2, Delay: time.Second},
				{Status: 503, MaxRetries: 1, Delay: 250 * time.Millisecond},
				{Status: 500, MaxRetries: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			service := newSlowdownService(t, tt.rows...)
			got := service.ResolveRetryRules(ctx, tt.requested, gpt4o, tt.route)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateRetryRules(t *testing.T) {
	tests := []struct {
		name    string
		vm      VirtualModel
		wantErr string
	}{
		{name: "unset", vm: VirtualModel{}},
		{name: "valid", vm: VirtualModel{RetryRules: retryRules(
			RetryRule{Status: 429, MaxRetries: new(2), Delay: time.Second},
			RetryRule{Status: 503, MaxRetries: new(1), Delay: 250 * time.Millisecond},
			RetryRule{Status: 500, MaxRetries: new(0)},
		)}},
		{name: "missing retries", vm: VirtualModel{RetryRules: retryRules(RetryRule{Status: 429})}, wantErr: "retry_rules[0].max_retries"},
		{name: "too many retries", vm: VirtualModel{RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(MaxRetryRuleRetries + 1)})}, wantErr: "retry_rules[0].max_retries"},
		{name: "too long a delay", vm: VirtualModel{RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(1), Delay: MaxRetryRuleDelay + time.Second})}, wantErr: "retry_rules[0].delay"},
		{name: "invalid status", vm: VirtualModel{RetryRules: retryRules(RetryRule{Status: 99})}, wantErr: "retry_rules[0].status"},
		{name: "negative retries", vm: VirtualModel{RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(-1)})}, wantErr: "retry_rules[0].max_retries"},
		{name: "negative delay", vm: VirtualModel{RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(1), Delay: -time.Second})}, wantErr: "retry_rules[0].delay"},
		{name: "duplicate status", vm: VirtualModel{RetryRules: retryRules(
			RetryRule{Status: 429, MaxRetries: new(1)}, RetryRule{Status: 429, MaxRetries: new(1)},
		)}, wantErr: "retry_rules[1].status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRetryRules(tt.vm)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRetryRulesRejectedOnProviderWidePolicy(t *testing.T) {
	_, err := normalizePolicyInput(testCatalog(), VirtualModel{
		Source: "openai/", Enabled: true,
		RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(2)}),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can only be configured for a model or redirect")
}

func TestVirtualModelCloneDoesNotShareRetryRules(t *testing.T) {
	original := VirtualModel{RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(2)})}
	cloned := original.clone()
	*cloned.RetryRules[0].MaxRetries = 5
	assert.Equal(t, 2, *original.RetryRules[0].MaxRetries)
}

func TestResolveRetryRulesFollowsRecordedRoute(t *testing.T) {
	service := newSlowdownService(t,
		VirtualModel{Source: "outer", Targets: []Target{{Model: "inner"}}, Enabled: true},
		VirtualModel{Source: "inner", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
			RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(4)}), Enabled: true},
	)
	requested := core.NewRequestedModelSelector("outer", "")
	resolved, route, changed, err := service.ResolveModelRouteForUserPath(context.Background(), requested)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, []string{"outer", "inner"}, route)
	got := service.ResolveRetryRules(context.Background(), requested, resolved, route)
	assert.Equal(t, core.UpstreamRetryRules{{Status: 429, MaxRetries: 4}}, got)
}

func TestResolveAttemptRetryRulesUsesFailoverTargetPolicies(t *testing.T) {
	service := newSlowdownService(t,
		VirtualModel{
			Source:     "extract",
			Targets:    []Target{{Provider: "openai", Model: "gpt-4o"}, {Provider: "anthropic", Model: "claude"}},
			RetryRules: retryRules(RetryRule{Status: 429, MaxRetries: new(3)}),
			Enabled:    true,
		},
		VirtualModel{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", RetryRules: retryRules(RetryRule{Status: 500, MaxRetries: new(4)}), Enabled: true},
		VirtualModel{Source: "anthropic/claude", ProviderName: "anthropic", Model: "claude", RetryRules: retryRules(RetryRule{Status: 503, MaxRetries: new(1)}), Enabled: true},
	)
	primary := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	backup := core.ModelSelector{Provider: "anthropic", Model: "claude"}
	resolution := &core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("extract", ""),
		ResolvedSelector: primary,
		VirtualModelPath: []string{"extract"},
	}

	got := service.ResolveAttemptRetryRules(context.Background(), resolution, backup)
	assert.Equal(t, core.UpstreamRetryRules{
		{Status: 429, MaxRetries: 3},
		{Status: 503, MaxRetries: 1},
	}, got)
	assert.NotContains(t, got, core.UpstreamRetryRule{Status: 500, MaxRetries: 4})
}
