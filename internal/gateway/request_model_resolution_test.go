package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"gomodel/internal/core"
)

type requestRefreshProvider struct {
	supported           map[string]bool
	providerType        map[string]string
	modelCount          int
	refreshErr          error
	resolveErrWhenEmpty bool
	refreshCalls        int
}

func newRequestRefreshProvider(modelCount int) *requestRefreshProvider {
	return &requestRefreshProvider{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerType: map[string]string{
			"openai/gpt-4o": "openai",
		},
		modelCount: modelCount,
	}
}

func (p *requestRefreshProvider) RefreshProviderModels(_ context.Context, providerSelector string) (int, error) {
	p.refreshCalls++
	if p.refreshErr != nil {
		return 0, p.refreshErr
	}
	if providerSelector != "ollama" {
		return 0, nil
	}
	p.supported["ollama/qwen3:8b"] = true
	p.providerType["ollama/qwen3:8b"] = "ollama"
	p.modelCount = 1
	return 1, nil
}

func (p *requestRefreshProvider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if p.resolveErrWhenEmpty && p.modelCount == 0 {
		return core.ModelSelector{}, false, core.NewProviderError("", http.StatusServiceUnavailable, "model registry not initialized", nil)
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func (p *requestRefreshProvider) Supports(model string) bool {
	return p.supported[model]
}

func (p *requestRefreshProvider) GetProviderType(model string) string {
	return p.providerType[model]
}

func (p *requestRefreshProvider) ModelCount() int {
	return p.modelCount
}

func (p *requestRefreshProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *requestRefreshProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *requestRefreshProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *requestRefreshProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *requestRefreshProvider) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *requestRefreshProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

type requestAliasResolver map[string]core.ModelSelector

func (r requestAliasResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if selector, ok := r[requested.RequestedQualifiedModel()]; ok {
		return selector, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

type requestRefreshTargetResolver struct {
	provider *requestRefreshProvider
	target   core.ModelSelector
	err      error
}

func (r requestRefreshTargetResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if requested.RequestedQualifiedModel() == "smart" && r.provider.Supports(r.target.QualifiedModel()) {
		return r.target, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

func (r requestRefreshTargetResolver) ResolveRefreshTarget(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if requested.RequestedQualifiedModel() != "smart" {
		return core.ModelSelector{}, false, nil
	}
	if r.err != nil {
		return core.ModelSelector{}, false, r.err
	}
	return r.target, true, nil
}

func TestResolveRequestModelRefreshesBeforeUnsupportedModel(t *testing.T) {
	provider := newRequestRefreshProvider(1)

	resolution, err := ResolveRequestModelWithAuthorizer(
		context.Background(),
		provider,
		nil,
		nil,
		core.NewRequestedModelSelector("ollama/qwen3:8b", ""),
	)
	if err != nil {
		t.Fatalf("ResolveRequestModelWithAuthorizer() error = %v, want nil", err)
	}
	if provider.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", provider.refreshCalls)
	}
	if got := resolution.ResolvedQualifiedModel(); got != "ollama/qwen3:8b" {
		t.Fatalf("ResolvedQualifiedModel() = %q, want ollama/qwen3:8b", got)
	}
	if got := resolution.ProviderType; got != "ollama" {
		t.Fatalf("ProviderType = %q, want ollama", got)
	}
}

func TestResolveRequestModelRefreshesBeforeEmptyRegistryFailure(t *testing.T) {
	provider := newRequestRefreshProvider(0)

	resolution, err := ResolveRequestModelWithAuthorizer(
		context.Background(),
		provider,
		nil,
		nil,
		core.NewRequestedModelSelector("ollama/qwen3:8b", ""),
	)
	if err != nil {
		t.Fatalf("ResolveRequestModelWithAuthorizer() error = %v, want nil", err)
	}
	if provider.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", provider.refreshCalls)
	}
	if got := resolution.ResolvedQualifiedModel(); got != "ollama/qwen3:8b" {
		t.Fatalf("ResolvedQualifiedModel() = %q, want ollama/qwen3:8b", got)
	}
}

func TestResolveRequestModelRefreshesAliasTargetBeforeCatalogSupportsIt(t *testing.T) {
	provider := newRequestRefreshProvider(1)
	resolver := requestRefreshTargetResolver{
		provider: provider,
		target:   core.ModelSelector{Provider: "ollama", Model: "qwen3:8b"},
	}

	resolution, err := ResolveRequestModelWithAuthorizer(
		context.Background(),
		provider,
		resolver,
		nil,
		core.NewRequestedModelSelector("smart", ""),
	)
	if err != nil {
		t.Fatalf("ResolveRequestModelWithAuthorizer() error = %v, want nil", err)
	}
	if provider.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", provider.refreshCalls)
	}
	if got := resolution.ResolvedQualifiedModel(); got != "ollama/qwen3:8b" {
		t.Fatalf("ResolvedQualifiedModel() = %q, want ollama/qwen3:8b", got)
	}
	if !resolution.AliasApplied {
		t.Fatal("AliasApplied = false, want true")
	}
}

func TestResolveRequestModelReturnsRefreshTargetError(t *testing.T) {
	provider := newRequestRefreshProvider(1)
	targetErr := errors.New("invalid alias target")
	resolver := requestRefreshTargetResolver{
		provider: provider,
		target:   core.ModelSelector{Provider: "ollama", Model: "qwen3:8b"},
		err:      targetErr,
	}

	_, err := ResolveRequestModelWithAuthorizer(
		context.Background(),
		provider,
		resolver,
		nil,
		core.NewRequestedModelSelector("smart", ""),
	)
	if err == nil {
		t.Fatal("ResolveRequestModelWithAuthorizer() error = nil, want refresh target error")
	}
	if !errors.Is(err, targetErr) {
		t.Fatalf("ResolveRequestModelWithAuthorizer() error = %v, want %v", err, targetErr)
	}
	if provider.refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0 after refresh target error", provider.refreshCalls)
	}
}

func TestResolveRequestModelRefreshesAliasTargetAfterResolverFailure(t *testing.T) {
	provider := newRequestRefreshProvider(0)
	provider.resolveErrWhenEmpty = true
	resolver := requestAliasResolver{
		"smart": {Provider: "ollama", Model: "qwen3:8b"},
	}

	resolution, err := ResolveRequestModelWithAuthorizer(
		context.Background(),
		provider,
		resolver,
		nil,
		core.NewRequestedModelSelector("smart", ""),
	)
	if err != nil {
		t.Fatalf("ResolveRequestModelWithAuthorizer() error = %v, want nil", err)
	}
	if provider.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", provider.refreshCalls)
	}
	if got := resolution.ResolvedQualifiedModel(); got != "ollama/qwen3:8b" {
		t.Fatalf("ResolvedQualifiedModel() = %q, want ollama/qwen3:8b", got)
	}
	if !resolution.AliasApplied {
		t.Fatal("AliasApplied = false, want true")
	}
}

func TestResolveRequestModelReturnsRefreshError(t *testing.T) {
	provider := newRequestRefreshProvider(1)
	provider.refreshErr = core.NewProviderError("ollama", http.StatusServiceUnavailable, "provider is unavailable", errors.New("connection refused"))

	_, err := ResolveRequestModelWithAuthorizer(
		context.Background(),
		provider,
		nil,
		nil,
		core.NewRequestedModelSelector("ollama/qwen3:8b", ""),
	)
	if err == nil {
		t.Fatal("ResolveRequestModelWithAuthorizer() error = nil, want refresh error")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want GatewayError", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusServiceUnavailable)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("error type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
}
