package core

import "testing"

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
			if got := tt.in.RequestedQualifiedModel(); got != tt.want {
				t.Fatalf("RequestedQualifiedModel() = %q, want %q", got, tt.want)
			}
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
			if got := tt.in.ResolvedRouteModel(); got != tt.want {
				t.Fatalf("ResolvedRouteModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequestModelResolutionCacheDerivedSelectors(t *testing.T) {
	resolution := &RequestModelResolution{
		ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
		ProviderName:     "openai-primary",
	}
	resolution.CacheDerivedSelectors()

	if got := resolution.ResolvedQualifiedModel(); got != "openai/gpt-4o" {
		t.Fatalf("ResolvedQualifiedModel() = %q, want openai/gpt-4o", got)
	}
	if got := resolution.ResolvedRouteModel(); got != "openai-primary/gpt-4o" {
		t.Fatalf("ResolvedRouteModel() = %q, want openai-primary/gpt-4o", got)
	}

	// Without the cache the getters compute the same values.
	uncached := &RequestModelResolution{
		ResolvedSelector: ModelSelector{Provider: "openai", Model: "gpt-4o"},
		ProviderName:     "openai-primary",
	}
	if got := uncached.ResolvedQualifiedModel(); got != resolution.ResolvedQualifiedModel() {
		t.Fatalf("uncached ResolvedQualifiedModel() = %q, want %q", got, resolution.ResolvedQualifiedModel())
	}
	if got := uncached.ResolvedRouteModel(); got != resolution.ResolvedRouteModel() {
		t.Fatalf("uncached ResolvedRouteModel() = %q, want %q", got, resolution.ResolvedRouteModel())
	}

	(*RequestModelResolution)(nil).CacheDerivedSelectors()
}
