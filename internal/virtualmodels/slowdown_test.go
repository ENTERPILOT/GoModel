package virtualmodels

import (
	"context"
	"math"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestResolveSlowdown(t *testing.T) {
	concrete := VirtualModel{
		Source:       "openai/gpt-4o",
		ProviderName: "openai",
		Model:        "gpt-4o",
		Slowdown:     new(0.2),
		Enabled:      true,
	}
	tests := []struct {
		name      string
		rows      []VirtualModel
		requested core.RequestedModelSelector
		resolved  core.ModelSelector
		ctx       context.Context
		want      float64
	}{
		{
			name: "alias overrides concrete model",
			rows: []VirtualModel{
				{Source: "slow-alias", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Slowdown: new(0.4), Enabled: true},
				concrete,
			},
			requested: core.NewRequestedModelSelector("slow-alias", ""),
			resolved:  core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			ctx:       context.Background(),
			want:      0.4,
		},
		{
			name: "alias inherits concrete model when omitted",
			rows: []VirtualModel{
				{Source: "plain-alias", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true},
				concrete,
			},
			requested: core.NewRequestedModelSelector("plain-alias", ""),
			resolved:  core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			ctx:       context.Background(),
			want:      0.2,
		},
		{
			name: "explicit alias zero disables concrete model slowdown",
			rows: []VirtualModel{
				{Source: "fast-alias", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Slowdown: new(0.0), Enabled: true},
				concrete,
			},
			requested: core.NewRequestedModelSelector("fast-alias", ""),
			resolved:  core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			ctx:       context.Background(),
			want:      0,
		},
		{
			name: "matching user path",
			rows: []VirtualModel{{
				Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o",
				UserPaths: []string{"/team/alpha"}, Slowdown: new(0.3), Enabled: true,
			}},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			resolved:  core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/alpha/member"),
			want:      0.3,
		},
		{
			name: "non-matching user path",
			rows: []VirtualModel{{
				Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o",
				UserPaths: []string{"/team/alpha"}, Slowdown: new(0.3), Enabled: true,
			}},
			requested: core.NewRequestedModelSelector("openai/gpt-4o", ""),
			resolved:  core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			ctx:       core.WithEffectiveUserPath(context.Background(), "/team/beta"),
			want:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSlowdownService(t, tt.rows...)
			if got := service.ResolveSlowdown(tt.ctx, tt.requested, tt.resolved); got != tt.want {
				t.Fatalf("ResolveSlowdown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVirtualModelCloneDoesNotShareSlowdown(t *testing.T) {
	original := VirtualModel{Slowdown: new(0.5)}
	cloned := original.clone()

	*cloned.Slowdown = 1
	if *original.Slowdown != 0.5 {
		t.Fatalf("mutating clone changed original slowdown to %v", *original.Slowdown)
	}
}

func TestUpsertValidatesSlowdown(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		slowdown    *float64
		wantInvalid bool
	}{
		{name: "omitted", source: "openai/gpt-4o", slowdown: nil},
		{name: "disabled zero", source: "openai/gpt-4o", slowdown: new(0.0)},
		{name: "minimum", source: "openai/gpt-4o", slowdown: new(MinSlowdownFactor)},
		{name: "maximum", source: "openai/gpt-4o", slowdown: new(MaxSlowdownFactor)},
		{name: "below minimum", source: "openai/gpt-4o", slowdown: new(0.09), wantInvalid: true},
		{name: "above maximum", source: "openai/gpt-4o", slowdown: new(10.01), wantInvalid: true},
		{name: "NaN", source: "openai/gpt-4o", slowdown: new(math.NaN()), wantInvalid: true},
		{name: "positive infinity", source: "openai/gpt-4o", slowdown: new(math.Inf(1)), wantInvalid: true},
		{name: "negative infinity", source: "openai/gpt-4o", slowdown: new(math.Inf(-1)), wantInvalid: true},
		{name: "provider scope", source: "openai/", slowdown: new(0.5), wantInvalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSlowdownService(t)
			err := service.Upsert(context.Background(), VirtualModel{
				Source:   tt.source,
				Slowdown: tt.slowdown,
				Enabled:  true,
			})
			if tt.wantInvalid {
				if err == nil || !IsValidationError(err) {
					t.Fatalf("Upsert() error = %v, want validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Upsert() error = %v, want nil", err)
			}
		})
	}
}

func newSlowdownService(t *testing.T, rows ...VirtualModel) *Service {
	t.Helper()
	store := newSQLVMStore(t)
	ctx := context.Background()
	for _, row := range rows {
		if err := store.Upsert(ctx, row); err != nil {
			t.Fatalf("store.Upsert(%q): %v", row.Source, err)
		}
	}
	service, err := NewService(store, testCatalog(), true)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := service.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return service
}
