package chutes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestListModels_PreservesChutesMetadata(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"chutes-model-catalog",
			"data":[{
				"id":"Qwen/Qwen3.5-397B-A17B-TEE",
				"owned_by":"sglang",
				"created":1677652288,
				"context_length":262144,
				"max_output_length":65536,
				"input_modalities":["text","image"],
				"supported_features":["json_mode","tools","structured_outputs","reasoning"],
				"confidential_compute":true,
				"pricing":{"prompt":0.45,"completion":3.0,"input_cache_read":0.045}
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/models" || gotAuth != "Bearer cpk_test" {
		t.Fatalf("request method/path/auth = %q/%q/%q, want GET /models/Bearer cpk_test", gotMethod, gotPath, gotAuth)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(resp.Data) = %d, want 1", len(resp.Data))
	}
	if resp.Object != "list" {
		t.Fatalf("resp.Object = %q, want list", resp.Object)
	}
	model := resp.Data[0]
	if model.Object != "model" {
		t.Fatalf("model.Object = %q, want model", model.Object)
	}
	if model.Metadata == nil || model.Metadata.ContextWindow == nil || *model.Metadata.ContextWindow != 262144 {
		t.Fatalf("model context metadata = %+v, want 262144", model.Metadata)
	}
	if model.Metadata.MaxOutputTokens == nil || *model.Metadata.MaxOutputTokens != 65536 {
		t.Fatalf("max output tokens = %+v, want 65536", model.Metadata.MaxOutputTokens)
	}
	if !model.Metadata.Capabilities["tools"] || !model.Metadata.Capabilities["vision"] || !model.Metadata.Capabilities["confidential_compute"] {
		t.Fatalf("capabilities = %v, want tools, vision, and confidential_compute", model.Metadata.Capabilities)
	}
	pricing := model.Metadata.Pricing
	if pricing == nil || pricing.Currency != "USD" || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 0.45 ||
		pricing.OutputPerMtok == nil || *pricing.OutputPerMtok != 3.0 ||
		pricing.CachedInputPerMtok == nil || *pricing.CachedInputPerMtok != 0.045 {
		t.Fatalf("pricing = %+v, want Chutes per-MTok USD pricing", pricing)
	}
}

func TestListModels_FiltersBlankIDsAndKeepsMinimalModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data":[
				{"id":"   "},
				{"id":" minimal-model ","object":"model","owned_by":" chutes ","pricing":{}}
			]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models = %+v, want one non-blank model", resp.Data)
	}
	model := resp.Data[0]
	if model.ID != "minimal-model" || model.Object != "model" || model.OwnedBy != "chutes" {
		t.Fatalf("model identity = %+v, want trimmed minimal model", model)
	}
	if model.Metadata.ContextWindow != nil || model.Metadata.MaxOutputTokens != nil ||
		model.Metadata.Capabilities != nil || model.Metadata.Pricing != nil {
		t.Fatalf("optional metadata = %+v, want omitted zero values", model.Metadata)
	}
}

func TestListModels_ReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"catalog unavailable"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels() error = nil, want upstream error")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok || gatewayErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v, want 503 *core.GatewayError", err)
	}
}

func TestModelCapabilities_MapsOptionalModalities(t *testing.T) {
	capabilities := modelCapabilities(modelInfo{
		SupportedFeatures: []string{" JSON_Mode ", "   "},
		InputModalities:   []string{"audio", "video", "unknown"},
	})
	if !capabilities["json_mode"] || !capabilities["audio"] || !capabilities["video"] {
		t.Fatalf("capabilities = %v, want normalized json_mode, audio, and video", capabilities)
	}
}

func TestModelPricing_HandlesNilAndPartialPrices(t *testing.T) {
	var absent *modelPricing
	if got := absent.toCore(); got != nil {
		t.Fatalf("nil pricing = %+v, want nil", got)
	}

	prompt := 0.25
	got := (&modelPricing{Prompt: &prompt}).toCore()
	if got == nil || got.InputPerMtok == nil || *got.InputPerMtok != prompt {
		t.Fatalf("partial pricing = %+v, want input price", got)
	}
	if got.OutputPerMtok != nil || got.CachedInputPerMtok != nil {
		t.Fatalf("partial pricing = %+v, want absent optional prices", got)
	}
}
