package virtualmodels

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTimeouts(t *testing.T) {
	gpt4o := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	exact := VirtualModel{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", Timeout: new(20), Enabled: true}
	wide := VirtualModel{Source: "gpt-4o", Model: "gpt-4o", Timeout: new(90), ResponseHeaderTimeout: new(15), Enabled: true}
	alias := func(source string, timeout, header *int) VirtualModel {
		return VirtualModel{Source: source, Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Timeout: timeout, ResponseHeaderTimeout: header, Enabled: true}
	}
	tests := []struct {
		name      string
		rows      []VirtualModel
		requested core.RequestedModelSelector
		route     []string
		ctx       context.Context
		want      core.UpstreamTimeouts
	}{
		{
			name:      "nothing configured inherits",
			rows:      []VirtualModel{alias("plain", nil, nil)},
			requested: core.NewRequestedModelSelector("plain", ""),
			want:      core.UpstreamTimeouts{},
		},
		{
			name:      "alias overrides model policy",
			rows:      []VirtualModel{alias("extract", new(300), nil), exact},
			requested: core.NewRequestedModelSelector("extract", ""),
			want:      core.UpstreamTimeouts{Timeout: 300 * time.Second},
		},
		{
			name:      "fields resolve independently",
			rows:      []VirtualModel{alias("extract", new(300), nil), wide},
			requested: core.NewRequestedModelSelector("extract", ""),
			want:      core.UpstreamTimeouts{Timeout: 300 * time.Second, ResponseHeaderTimeout: 15 * time.Second},
		},
		{
			name:      "alias inherits model policy when omitted",
			rows:      []VirtualModel{alias("plain", nil, nil), exact},
			requested: core.NewRequestedModelSelector("plain", ""),
			want:      core.UpstreamTimeouts{Timeout: 20 * time.Second},
		},
		{
			name:      "exact beats model-wide per field",
			rows:      []VirtualModel{exact, wide},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			want:      core.UpstreamTimeouts{Timeout: 20 * time.Second, ResponseHeaderTimeout: 15 * time.Second},
		},
		{
			name: "outer redirect on the route wins over inner",
			rows: []VirtualModel{
				{Source: "outer", Targets: []Target{{Model: "inner"}}, ResponseHeaderTimeout: new(5), Enabled: true},
				alias("inner", new(60), new(10)),
			},
			requested: core.NewRequestedModelSelector("outer", ""),
			route:     []string{"outer", "inner"},
			want:      core.UpstreamTimeouts{Timeout: 60 * time.Second, ResponseHeaderTimeout: 5 * time.Second},
		},
		{
			name: "policy for another user path is skipped",
			rows: []VirtualModel{
				{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/team/alpha"}, Timeout: new(20), Enabled: true},
			},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/beta"),
			want:      core.UpstreamTimeouts{},
		},
		{
			name:      "explicit provider skips redirects",
			rows:      []VirtualModel{alias("gpt-4o-fast", new(300), nil), exact},
			requested: core.NewRequestedModelSelector("gpt-4o", "openai"),
			want:      core.UpstreamTimeouts{Timeout: 20 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			service := newSlowdownService(t, tt.rows...)
			got := service.ResolveTimeouts(ctx, tt.requested, gpt4o, tt.route)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		vm      VirtualModel
		wantErr string
	}{
		{name: "unset", vm: VirtualModel{}},
		{name: "bounds", vm: VirtualModel{Timeout: new(1), ResponseHeaderTimeout: new(MaxTimeoutSeconds)}},
		{name: "zero", vm: VirtualModel{Timeout: new(0)}, wantErr: "timeout must be between 1 and 86400 seconds"},
		{name: "negative header", vm: VirtualModel{ResponseHeaderTimeout: new(-1)}, wantErr: "response_header_timeout must be between 1 and 86400 seconds"},
		{name: "too long", vm: VirtualModel{Timeout: new(MaxTimeoutSeconds + 1)}, wantErr: "timeout must be between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTimeouts(tt.vm)
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

func TestTimeoutsRejectedOnProviderWidePolicy(t *testing.T) {
	_, err := normalizePolicyInput(testCatalog(), VirtualModel{Source: "openai/", Timeout: new(30), Enabled: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can only be configured for a model or redirect")
}

func TestVirtualModelCloneDoesNotShareTimeouts(t *testing.T) {
	original := VirtualModel{Timeout: new(30), ResponseHeaderTimeout: new(10)}
	cloned := original.clone()

	*cloned.Timeout = 1
	*cloned.ResponseHeaderTimeout = 1
	assert.Equal(t, 30, *original.Timeout)
	assert.Equal(t, 10, *original.ResponseHeaderTimeout)
}
