package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func TestPassthroughExecutionTarget_PrefersWorkflow(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/responses?trace=1", nil)
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai",
			RawEndpoint:        "v1/responses",
			NormalizedEndpoint: "responses",
			AuditPath:          "/v1/responses",
		},
	}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	providerType, endpoint, info, err := passthroughExecutionTarget(c, nil, false)
	if err != nil {
		t.Fatalf("passthroughExecutionTarget() error = %v", err)
	}
	if providerType != "openai" {
		t.Fatalf("providerType = %q, want openai", providerType)
	}
	if endpoint != "responses?trace=1" {
		t.Fatalf("endpoint = %q, want responses?trace=1", endpoint)
	}
	if info == nil {
		t.Fatal("info = nil")
	}
	if info.NormalizedEndpoint != "responses" {
		t.Fatalf("NormalizedEndpoint = %q, want responses", info.NormalizedEndpoint)
	}
}

func TestPassthroughExecutionTarget_NormalizesFallbackFromPath(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/responses?trace=1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	providerType, endpoint, info, err := passthroughExecutionTarget(c, nil, true)
	if err != nil {
		t.Fatalf("passthroughExecutionTarget() error = %v", err)
	}
	if providerType != "openai" {
		t.Fatalf("providerType = %q, want openai", providerType)
	}
	if endpoint != "responses?trace=1" {
		t.Fatalf("endpoint = %q, want responses?trace=1", endpoint)
	}
	if info == nil {
		t.Fatal("info = nil")
	}
	if info.NormalizedEndpoint != "responses" {
		t.Fatalf("NormalizedEndpoint = %q, want responses", info.NormalizedEndpoint)
	}
}

func TestPassthroughExecutionTarget_ResolvesConfiguredProviderNameToType(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/openai_test/v1/responses?trace=1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	provider := &mockProvider{
		providerTypes: map[string]string{
			"openai_test/gpt-5-mini": "openai",
		},
		providerNames: map[string]string{
			"openai_test/gpt-5-mini": "openai_test",
		},
	}

	providerType, endpoint, info, err := passthroughExecutionTarget(c, provider, true)
	if err != nil {
		t.Fatalf("passthroughExecutionTarget() error = %v", err)
	}
	if providerType != "openai" {
		t.Fatalf("providerType = %q, want openai", providerType)
	}
	if endpoint != "responses?trace=1" {
		t.Fatalf("endpoint = %q, want responses?trace=1", endpoint)
	}
	if info == nil || info.Provider != "openai" {
		t.Fatalf("info.Provider = %#v, want openai", info)
	}
}
