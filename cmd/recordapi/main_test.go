package main

import (
	"net/http"
	"testing"
)

// TestKimiProviderConfig verifies the Kimi provider entry has the expected
// base URL, env key, auth header, and required custom headers.
func TestKimiProviderConfig(t *testing.T) {
	cfg, ok := providerConfigs["kimi"]
	if !ok {
		t.Fatalf("providerConfigs[\"kimi\"] missing")
	}
	if cfg.baseURL != "https://api.kimi.com/coding" {
		t.Errorf("baseURL = %q, want %q", cfg.baseURL, "https://api.kimi.com/coding")
	}
	if cfg.envKey != "KIMI_API_KEY" {
		t.Errorf("envKey = %q, want %q", cfg.envKey, "KIMI_API_KEY")
	}
	if cfg.authHeader != "Authorization" {
		t.Errorf("authHeader = %q, want %q", cfg.authHeader, "Authorization")
	}
	if cfg.customHeaders == nil {
		t.Fatalf("customHeaders is nil, want non-nil map")
	}
	for _, key := range []string{"User-Agent", "X-Title", "Http-Referer"} {
		if _, ok := cfg.customHeaders[key]; !ok {
			t.Errorf("customHeaders missing required key %q", key)
		}
	}
}

// TestEmbeddingsEndpointConfig verifies the embeddings endpoint entry exposes
// the expected path, method, and request body fields.
func TestEmbeddingsEndpointConfig(t *testing.T) {
	cfg, ok := endpointConfigs["embeddings"]
	if !ok {
		t.Fatalf("endpointConfigs[\"embeddings\"] missing")
	}
	if cfg.path != "/v1/embeddings" {
		t.Errorf("path = %q, want %q", cfg.path, "/v1/embeddings")
	}
	if cfg.method != http.MethodPost {
		t.Errorf("method = %q, want %q", cfg.method, http.MethodPost)
	}
	if got, want := cfg.requestBody["model"], "text-embedding-3-small"; got != want {
		t.Errorf("requestBody[model] = %v, want %v", got, want)
	}
	if got, want := cfg.requestBody["input"], "hello world"; got != want {
		t.Errorf("requestBody[input] = %v, want %v", got, want)
	}
}

// TestKimiResponsesCapability verifies Kimi is marked as not supporting the
// responses endpoint in the provider capability map.
func TestKimiResponsesCapability(t *testing.T) {
	caps, ok := providerCapabilities["kimi"]
	if !ok {
		t.Fatalf("providerCapabilities[\"kimi\"] missing")
	}
	if caps["responses"] != false {
		t.Errorf("providerCapabilities[\"kimi\"][\"responses\"] = %v, want false", caps["responses"])
	}
}

// TestLegacyProvidersHaveNilCustomHeaders verifies the legacy provider entries
// (openai, groq) did not accidentally grow a customHeaders map.
func TestLegacyProvidersHaveNilCustomHeaders(t *testing.T) {
	for _, name := range []string{"openai", "groq"} {
		cfg, ok := providerConfigs[name]
		if !ok {
			t.Errorf("providerConfigs[%q] missing", name)
			continue
		}
		if cfg.customHeaders != nil {
			t.Errorf("providerConfigs[%q].customHeaders = %v, want nil", name, cfg.customHeaders)
		}
	}
}
