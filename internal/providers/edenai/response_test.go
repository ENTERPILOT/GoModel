package edenai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// edenChatResponse is a verbatim non-streaming Eden /v3/chat/completions body.
// Note where Eden puts its extensions: `cost` and `provider` sit at the
// response root, not inside `usage`.
const edenChatResponse = `{
	"status": "success",
	"id": "chatcmpl-eden",
	"created": 1741015112,
	"model": "gpt-4o-mini-2024-07-18",
	"object": "chat.completion",
	"choices": [{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	"usage": {
		"completion_tokens": 99,
		"prompt_tokens": 1170,
		"total_tokens": 1269
	},
	"service_tier": "default",
	"cost": 0.0002349,
	"provider": "openai"
}`

func chatResponseFrom(t *testing.T, payload string) *core.ChatResponse {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	return resp
}

// TestChatCompletion_LiftsRootCostIntoRawUsage is the load-bearing test for
// exact-cost accounting: Eden reports cost at the response root, but
// internal/usage builds its rawData from the usage object, so the provider
// must relocate it for the existing cost path to see it.
func TestChatCompletion_LiftsRootCostIntoRawUsage(t *testing.T) {
	resp := chatResponseFrom(t, edenChatResponse)

	cost, ok := resp.Usage.RawUsage["cost"]
	if !ok {
		t.Fatalf("Usage.RawUsage = %v, want Eden's root-level cost lifted in", resp.Usage.RawUsage)
	}
	value, ok := cost.(float64)
	if !ok || math.Abs(value-0.0002349) > 1e-12 {
		t.Fatalf("Usage.RawUsage[\"cost\"] = %#v, want 0.0002349", cost)
	}
	if resp.Usage.PromptTokens != 1170 || resp.Usage.CompletionTokens != 99 {
		t.Errorf("token counts = %+v, want the reported usage preserved", resp.Usage)
	}
}

// TestChatCompletion_KeepsCostVisibleToClients asserts lifting the value into
// RawUsage does not remove it from the response the client receives.
func TestChatCompletion_KeepsCostVisibleToClients(t *testing.T) {
	resp := chatResponseFrom(t, edenChatResponse)

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if cost, ok := decoded["cost"].(float64); !ok || math.Abs(cost-0.0002349) > 1e-12 {
		t.Errorf("serialized cost = %#v, want Eden's cost preserved for the client", decoded["cost"])
	}
}

// TestChatCompletion_RejectsUnusableCost asserts a cost that would corrupt
// accounting is dropped rather than lifted, leaving the request to fall back
// to token-derived pricing.
func TestChatCompletion_RejectsUnusableCost(t *testing.T) {
	tests := []struct {
		name string
		cost string
	}{
		{name: "absent", cost: ""},
		{name: "null", cost: `"cost": null,`},
		{name: "negative", cost: `"cost": -0.5,`},
		{name: "non-numeric", cost: `"cost": "free",`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := `{"id":"c","created":1,"model":"m","choices":[],` + tt.cost +
				`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
			resp := chatResponseFrom(t, payload)
			if _, ok := resp.Usage.RawUsage["cost"]; ok {
				t.Fatalf("Usage.RawUsage = %v, want no cost lifted for an unusable value", resp.Usage.RawUsage)
			}
		})
	}
}

// TestChatCompletion_UsageLevelCostWins asserts the conventional location
// takes precedence if Eden ever reports cost in both places.
func TestChatCompletion_UsageLevelCostWins(t *testing.T) {
	payload := `{"id":"c","created":1,"model":"m","choices":[],"cost":9.99,` +
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"cost":0.5}}`
	resp := chatResponseFrom(t, payload)

	value, ok := resp.Usage.RawUsage["cost"].(float64)
	if !ok || value != 0.5 {
		t.Fatalf("Usage.RawUsage[\"cost\"] = %#v, want the usage-level 0.5", resp.Usage.RawUsage["cost"])
	}
}

// TestChatCompletion_DoesNotReportEdenUpstreamAsExecutingProvider asserts
// Eden's "provider" member is cleared off the typed field. The gateway treats
// a populated ChatResponse.Provider as the provider it executed against
// (gateway.ResponseProviderType feeds attempt telemetry and failover
// metadata), so leaving Eden's upstream there would report "openai" for a
// request that ran through Eden. Clearing it lets the gateway fall back to
// the configured provider type.
func TestChatCompletion_DoesNotReportEdenUpstreamAsExecutingProvider(t *testing.T) {
	resp := chatResponseFrom(t, edenChatResponse)

	if resp.Provider != "" {
		t.Fatalf("Provider = %q, want empty so the gateway labels the request edenai", resp.Provider)
	}
}

// TestChatCompletion_PreservesEdenUpstreamProvider asserts the upstream is not
// simply discarded: it is re-exposed under an Eden-namespaced key that cannot
// collide with the typed provider member.
func TestChatCompletion_PreservesEdenUpstreamProvider(t *testing.T) {
	resp := chatResponseFrom(t, edenChatResponse)

	raw := resp.ExtraFields.Lookup(upstreamProviderField)
	if len(raw) == 0 {
		t.Fatalf("ExtraFields missing %q", upstreamProviderField)
	}
	var upstream string
	if err := json.Unmarshal(raw, &upstream); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", raw, err)
	}
	if upstream != "openai" {
		t.Errorf("%s = %q, want openai", upstreamProviderField, upstream)
	}
}

// TestChatCompletion_NoUpstreamProviderLeavesNoMarker asserts a response
// without Eden's provider member does not gain an empty namespaced key.
func TestChatCompletion_NoUpstreamProviderLeavesNoMarker(t *testing.T) {
	resp := chatResponseFrom(t, `{"id":"c","created":1,"model":"m","choices":[],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)

	if resp.Provider != "" {
		t.Errorf("Provider = %q, want empty", resp.Provider)
	}
	if raw := resp.ExtraFields.Lookup(upstreamProviderField); len(raw) != 0 {
		t.Errorf("ExtraFields[%q] = %s, want absent", upstreamProviderField, raw)
	}
}

// TestEmbeddings_DoesNotReportEdenUpstreamAsExecutingProvider applies the same
// reasoning to embeddings, which feed the same gateway provider labeling.
func TestEmbeddings_DoesNotReportEdenUpstreamAsExecutingProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],
			"model":"openai/text-embedding-3-small","provider":"openai","cost":0.0001,
			"usage":{"prompt_tokens":4,"total_tokens":4}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "openai/text-embedding-3-small",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Provider != "" {
		t.Fatalf("Provider = %q, want empty so the gateway labels the request edenai", resp.Provider)
	}
}

// TestResponses_InheritsCostLifting asserts the Responses surface picks up the
// same normalization, because it is translated through ChatCompletion.
func TestResponses_InheritsCostLifting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(edenChatResponse))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: slashedModel,
		Input: "hi",
	})
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	value, ok := resp.Usage.RawUsage["cost"].(float64)
	if !ok || math.Abs(value-0.0002349) > 1e-12 {
		t.Fatalf("Responses usage cost = %#v, want 0.0002349 carried through the chat translation", resp.Usage.RawUsage["cost"])
	}
}
