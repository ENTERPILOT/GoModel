package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestModelResolutionRequestedQualifiedModel(t *testing.T) {
	tests := []struct {
		name string
		in   *RequestModelResolution
		want string
	}{
		{
			name: "raw alias with slash and no explicit provider stays raw",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("anthropic/claude-opus-4-6", ""),
			},
			want: "anthropic/claude-opus-4-6",
		},
		{
			name: "explicit provider with provider-prefixed model normalizes once",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("openai/gpt-4o", "openai"),
			},
			want: "openai/gpt-4o",
		},
		{
			name: "explicit provider without prefix becomes qualified model",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("gpt-4o", "openai"),
			},
			want: "openai/gpt-4o",
		},
		{
			name: "explicit provider preserves raw slash model",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("openai/gpt-oss-120b", "groq"),
			},
			want: "groq/openai/gpt-oss-120b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.RequestedQualifiedModel()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRequestModelResolutionResolvedRouteModel(t *testing.T) {
	tests := []struct {
		name string
		in   *RequestModelResolution
		want string
	}{
		{name: "nil resolution", in: nil, want: ""},
		{name: "empty model", in: &RequestModelResolution{ProviderName: "openai-primary"}, want: ""},
		{
			name: "provider instance name wins over selector provider",
			in: &RequestModelResolution{
				ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
				ProviderName:     "openai-primary",
			},
			want: "openai-primary/gpt-4o",
		},
		{
			name: "selector provider is the fallback prefix",
			in: &RequestModelResolution{
				ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
			},
			want: "openai/gpt-4o",
		},
		{
			name: "bare model without any provider",
			in: &RequestModelResolution{
				ResolvedSelector: ModelSelector{Model: "gpt-4o"},
			},
			want: "gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.in.ResolvedRouteModel())
		})
	}
}

func TestRequestModelResolutionCacheDerivedSelectors(t *testing.T) {
	resolution := &RequestModelResolution{
		ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
		ProviderName:     "openai-primary",
	}
	resolution.CacheDerivedSelectors()

	assert.Equal(t, "openai/gpt-4o", resolution.ResolvedQualifiedModel())
	assert.Equal(t, "openai-primary/gpt-4o", resolution.ResolvedRouteModel())

	// Without the cache the getters compute the same values.
	uncached := &RequestModelResolution{
		ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
		ProviderName:     "openai-primary",
	}
	assert.Equal(t, resolution.ResolvedQualifiedModel(), uncached.ResolvedQualifiedModel())
	assert.Equal(t, resolution.ResolvedRouteModel(), uncached.ResolvedRouteModel())

	assert.NotPanics(t, func() { (*RequestModelResolution)(nil).CacheDerivedSelectors() })
}

// A copy rewritten after caching, like the failover clone a native dispatch
// builds, must report its own route rather than the memoized original.
func TestRequestModelResolutionCacheIgnoresRewrittenCopy(t *testing.T) {
	primary := &RequestModelResolution{
		ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
		ProviderName:     "openai-primary",
	}
	primary.CacheDerivedSelectors()

	failover := *primary
	failover.ResolvedSelector = ModelSelector{Provider: "anthropic", Model: "claude-sonnet-4-5"}
	failover.ProviderName = "anthropic-eu"

	assert.Equal(t, "anthropic/claude-sonnet-4-5", failover.ResolvedQualifiedModel())
	assert.Equal(t, "anthropic-eu/claude-sonnet-4-5", failover.ResolvedRouteModel())
	assert.Equal(t, "openai/gpt-4o", primary.ResolvedQualifiedModel())
	assert.Equal(t, "openai-primary/gpt-4o", primary.ResolvedRouteModel())

	renamed := *primary
	renamed.ProviderName = "openai-backup"
	assert.Equal(t, "openai-backup/gpt-4o", renamed.ResolvedRouteModel())
}
