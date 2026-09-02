package gateway

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestTranslatedSelectorRewriteRequired(t *testing.T) {
	tests := []struct {
		name       string
		resolution *core.RequestModelResolution
		want       bool
	}{
		{name: "nil resolution", want: true},
		{
			name: "bare model resolved to a configured provider forwards verbatim",
			resolution: &core.RequestModelResolution{
				Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			},
		},
		{
			name: "provider-qualified model is rewritten for the upstream",
			resolution: &core.RequestModelResolution{
				Requested:        core.NewRequestedModelSelector("openai/gpt-4o", ""),
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			},
			want: true,
		},
		{
			name: "alias is rewritten",
			resolution: &core.RequestModelResolution{
				Requested:        core.NewRequestedModelSelector("fast", ""),
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"},
				AliasApplied:     true,
			},
			want: true,
		},
		{
			name: "explicit provider hint is stripped by translation",
			resolution: &core.RequestModelResolution{
				Requested:        core.NewRequestedModelSelector("gpt-4o", "openai"),
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := translatedSelectorRewriteRequired(tt.resolution); got != tt.want {
				t.Fatalf("translatedSelectorRewriteRequired() = %v, want %v", got, tt.want)
			}
		})
	}
}

type planningProvider struct {
	core.RoutableProvider
	applies bool
}

func (p planningProvider) PromptCachePlanApplies(string, core.ModelSelector, *core.ChatRequest) bool {
	return p.applies
}

func TestCanFastPathChatPassthrough(t *testing.T) {
	workflow := func(providerType, providerHint string) *core.Workflow {
		return &core.Workflow{
			ProviderType: providerType,
			Resolution: &core.RequestModelResolution{
				Requested:        core.NewRequestedModelSelector("gpt-4o", providerHint),
				ResolvedSelector: core.ModelSelector{Provider: providerType, Model: "gpt-4o"},
				ProviderType:     providerType,
			},
		}
	}
	// Preparation writes the resolved provider into req.Provider before
	// dispatch, so every prepared request carries one; the gate must key on
	// the client's original hint instead.
	prepared := func(providerType string, stream bool) *core.ChatRequest {
		return &core.ChatRequest{Model: "gpt-4o", Provider: providerType, Stream: stream}
	}
	tests := []struct {
		name         string
		providerType string
		providerHint string
		req          *core.ChatRequest
		planApplies  bool
		enforceUsage bool
		want         bool
	}{
		{name: "non-streaming openai", providerType: "openai", req: prepared("openai", false), want: true},
		{name: "streaming openai", providerType: "openai", req: prepared("openai", true), want: true},
		// Usage enforcement only injects stream_options into streaming
		// requests; a non-streaming JSON response always carries usage.
		{name: "non-streaming openai with enforced usage", providerType: "openai", req: prepared("openai", false), enforceUsage: true, want: true},
		{name: "streaming openai with enforced usage", providerType: "openai", req: prepared("openai", true), enforceUsage: true},
		{name: "azure", providerType: "azure", req: prepared("azure", false), want: true},
		{name: "anthropic never proxied", providerType: "anthropic", req: prepared("anthropic", false)},
		{name: "planner would apply", providerType: "openai", req: prepared("openai", false), planApplies: true},
		{name: "client-supplied provider forces translation", providerType: "openai", providerHint: "openai", req: prepared("openai", false)},
		{name: "o-series with max_tokens forces translation", providerType: "openai", req: &core.ChatRequest{Model: "o3", Provider: "openai", MaxTokens: new(int)}},
		{name: "nil request", providerType: "openai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := InferenceConfig{Provider: planningProvider{applies: tt.planApplies}}
			if tt.enforceUsage {
				cfg.UsageLogger = &usageCaptureLogger{config: usage.Config{Enabled: true, EnforceReturningUsageData: true}}
			}
			o := NewInferenceOrchestrator(cfg)
			if got := o.CanFastPathChatPassthrough(workflow(tt.providerType, tt.providerHint), tt.req); got != tt.want {
				t.Fatalf("CanFastPathChatPassthrough() = %v, want %v", got, tt.want)
			}
		})
	}
}
