package server

import (
	"context"
	"io"
	"strings"
	"testing"

	"gomodel/internal/core"
)

type canonicalizingProvider struct {
	resolved map[string]core.ModelSelector
	types    map[string]string
	names    map[string]string
}

func (p *canonicalizingProvider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	key := requested.RequestedQualifiedModel()
	if selector, ok := p.resolved[key]; ok {
		return selector, selector.QualifiedModel() != key, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func (p *canonicalizingProvider) Supports(model string) bool {
	_, ok := p.types[model]
	return ok
}

func (p *canonicalizingProvider) GetProviderType(model string) string {
	return p.types[model]
}

func (p *canonicalizingProvider) GetProviderName(model string) string {
	return p.names[model]
}

func (p *canonicalizingProvider) GetProviderTypeForName(providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return ""
	}
	for qualifiedModel, candidate := range p.names {
		if strings.TrimSpace(candidate) != providerName {
			continue
		}
		return strings.TrimSpace(p.types[qualifiedModel])
	}
	return ""
}

func (p *canonicalizingProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *canonicalizingProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *canonicalizingProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *canonicalizingProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *canonicalizingProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *canonicalizingProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func TestResolveRequestModel_UsesResolvedProviderNameInsteadOfSelectorPrefix(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-nano"},
		providerTypes: map[string]string{
			"openai/gpt-5-nano": "openai",
		},
		providerNames: map[string]string{
			"openai/gpt-5-nano": "openai_test",
		},
	}

	resolution, err := resolveRequestModel(provider, nil, core.NewRequestedModelSelector("openai/gpt-5-nano", ""))
	if err != nil {
		t.Fatalf("resolveRequestModel() error = %v", err)
	}

	if got := resolution.ResolvedSelector.Provider; got != "openai" {
		t.Fatalf("ResolvedSelector.Provider = %q, want %q", got, "openai")
	}
	if got := resolution.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
}

func TestResolveRequestModel_CanonicalizesProviderTypeSelectorToConcreteProviderName(t *testing.T) {
	provider := &canonicalizingProvider{
		resolved: map[string]core.ModelSelector{
			"openai/gpt-5-nano": {Provider: "openai_test", Model: "gpt-5-nano"},
		},
		types: map[string]string{
			"openai_test/gpt-5-nano": "openai",
		},
		names: map[string]string{
			"openai_test/gpt-5-nano": "openai_test",
		},
	}

	resolution, err := resolveRequestModel(provider, nil, core.NewRequestedModelSelector("openai/gpt-5-nano", ""))
	if err != nil {
		t.Fatalf("resolveRequestModel() error = %v", err)
	}

	if got := resolution.ResolvedQualifiedModel(); got != "openai_test/gpt-5-nano" {
		t.Fatalf("ResolvedQualifiedModel = %q, want %q", got, "openai_test/gpt-5-nano")
	}
	if got := resolution.ProviderType; got != "openai" {
		t.Fatalf("ProviderType = %q, want %q", got, "openai")
	}
	if got := resolution.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
}

type aliasResolverStub struct{}

func (aliasResolverStub) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if requested.RequestedQualifiedModel() == "anthropic/claude-opus-4-6" {
		return core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"}, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func TestResolveRequestModel_CanonicalizesAliasOutputThroughProviderResolver(t *testing.T) {
	provider := &canonicalizingProvider{
		resolved: map[string]core.ModelSelector{
			"openai/gpt-5-nano": {Provider: "openai_test", Model: "gpt-5-nano"},
		},
		types: map[string]string{
			"openai_test/gpt-5-nano": "openai",
		},
		names: map[string]string{
			"openai_test/gpt-5-nano": "openai_test",
		},
	}

	resolution, err := resolveRequestModel(provider, aliasResolverStub{}, core.NewRequestedModelSelector("anthropic/claude-opus-4-6", ""))
	if err != nil {
		t.Fatalf("resolveRequestModel() error = %v", err)
	}

	if !resolution.AliasApplied {
		t.Fatal("AliasApplied = false, want true")
	}
	if got := resolution.ResolvedQualifiedModel(); got != "openai_test/gpt-5-nano" {
		t.Fatalf("ResolvedQualifiedModel = %q, want %q", got, "openai_test/gpt-5-nano")
	}
	if got := resolution.ProviderType; got != "openai" {
		t.Fatalf("ProviderType = %q, want %q", got, "openai")
	}
	if got := resolution.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
}
