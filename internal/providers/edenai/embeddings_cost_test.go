package edenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/usage"
)

// edenEmbeddingBody is the shape Eden documents for /v3/embeddings: the
// OpenAI-compatible envelope plus the two members Eden adds at the root, the
// upstream "provider" and the exact per-request "cost" in USD.
const edenEmbeddingBody = `{
	"object": "list",
	"model": "openai/text-embedding-3-small",
	"provider": "openai",
	"data": [{"object": "embedding", "index": 0, "embedding": [0.0123, -0.0456]}],
	"usage": {"prompt_tokens": 9, "total_tokens": 9},
	"cost": 0.0000012
}`

// embeddingsProvider serves one /embeddings payload.
func embeddingsProvider(t *testing.T, payload string) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
}

// TestEmbeddings_LiftsRootLevelCostIntoUsage asserts Eden's root-level
// embeddings cost survives decoding.
//
// core.EmbeddingResponse models no unknown-field container, so a root member
// is dropped outright unless the provider decodes the envelope itself. Landing
// the value in Usage.RawUsage is what puts it on the same path the chat
// surface already uses.
func TestEmbeddings_LiftsRootLevelCostIntoUsage(t *testing.T) {
	provider := embeddingsProvider(t, edenEmbeddingBody)

	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}

	cost, ok := resp.Usage.RawUsage["cost"]
	if !ok {
		t.Fatalf("Usage.RawUsage = %v, want Eden's root-level cost lifted into it", resp.Usage.RawUsage)
	}
	if cost != 0.0000012 {
		t.Errorf("RawUsage[cost] = %v, want 0.0000012 exactly", cost)
	}
	// The rest of the envelope must still decode normally.
	if resp.Usage.PromptTokens != 9 || resp.Usage.TotalTokens != 9 {
		t.Errorf("usage tokens = %d/%d, want 9/9", resp.Usage.PromptTokens, resp.Usage.TotalTokens)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Embedding) == 0 {
		t.Errorf("data = %+v, want one embedding", resp.Data)
	}
	if resp.Model != "openai/text-embedding-3-small" {
		t.Errorf("model = %q, want the Eden model ID forwarded verbatim", resp.Model)
	}
	// Eden's upstream name must not be reported as the executing provider.
	if resp.Provider != "" {
		t.Errorf("Provider = %q, want empty so the gateway reports edenai", resp.Provider)
	}
}

// TestEmbeddings_ExactCostReachesRecordedTotal is the end-to-end assertion for
// the whole chain: Eden's root-level cost, through the provider, through usage
// extraction, to the cost recorded against the request.
//
// Catalog pricing is supplied deliberately and is wrong on purpose. If the
// exact figure were lost anywhere along the way, the entry would silently fall
// back to these token rates and the recorded total would be a rate-card
// reconstruction rather than what Eden actually charged.
func TestEmbeddings_ExactCostReachesRecordedTotal(t *testing.T) {
	provider := embeddingsProvider(t, edenEmbeddingBody)

	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}

	wrongRate := 100.0 // $100/MTok would price 9 tokens at $0.0009, not $0.0000012.
	pricing := &core.ModelPricing{Currency: "USD", InputPerMtok: &wrongRate}

	entry := usage.ExtractFromEmbeddingResponse(resp, "req-1", providerType, "/v1/embeddings", pricing)
	if entry == nil {
		t.Fatal("ExtractFromEmbeddingResponse returned nil")
	}
	if entry.TotalCost == nil {
		t.Fatal("TotalCost = nil, want Eden's exact charge recorded")
	}
	if *entry.TotalCost != 0.0000012 {
		t.Errorf("TotalCost = %v, want 0.0000012 exactly (Eden's reported charge, not a token-rate estimate)", *entry.TotalCost)
	}
	if entry.CostSource != usage.CostSourceEdenAICost {
		t.Errorf("CostSource = %q, want %q", entry.CostSource, usage.CostSourceEdenAICost)
	}
	if entry.CostsCalculationCaveat != "" {
		t.Errorf("CostsCalculationCaveat = %q, want empty for an exact provider-reported cost", entry.CostsCalculationCaveat)
	}
}

// TestEmbeddings_FallsBackToTokenPricingWithoutCost asserts the fallback still
// works: an Eden embeddings response carrying no cost is priced from the
// discovered per-model rates, exactly as before.
func TestEmbeddings_FallsBackToTokenPricingWithoutCost(t *testing.T) {
	provider := embeddingsProvider(t, `{
		"object": "list",
		"model": "openai/text-embedding-3-small",
		"data": [{"object": "embedding", "index": 0, "embedding": [0.1]}],
		"usage": {"prompt_tokens": 1000, "total_tokens": 1000}
	}`)

	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if len(resp.Usage.RawUsage) != 0 {
		t.Errorf("Usage.RawUsage = %v, want empty when Eden reports no cost", resp.Usage.RawUsage)
	}

	rate := 0.02 // $0.02/MTok * 1000 tokens = $0.00002
	pricing := &core.ModelPricing{Currency: "USD", InputPerMtok: &rate}

	entry := usage.ExtractFromEmbeddingResponse(resp, "req-2", providerType, "/v1/embeddings", pricing)
	if entry == nil {
		t.Fatal("ExtractFromEmbeddingResponse returned nil")
	}
	if entry.TotalCost == nil {
		t.Fatal("TotalCost = nil, want the token-rate fallback to apply")
	}
	if *entry.TotalCost != 0.00002 {
		t.Errorf("TotalCost = %v, want 0.00002 from the catalog rate", *entry.TotalCost)
	}
	if entry.CostSource != usage.CostSourceModelPricing {
		t.Errorf("CostSource = %q, want %q", entry.CostSource, usage.CostSourceModelPricing)
	}
}

// TestEmbeddings_RejectsUnusableCost asserts a cost member that would corrupt
// accounting is ignored rather than recorded, leaving the token-rate fallback
// in charge.
//
// The null case is the one that matters most: unmarshalling a JSON null into a
// float64 succeeds without error, so an unscreened null would be recorded as a
// real $0.00 charge and understate spend.
func TestEmbeddings_RejectsUnusableCost(t *testing.T) {
	tests := []struct {
		name string
		cost string
	}{
		{"null", `null`},
		{"negative", `-0.5`},
		{"string", `"0.0000012"`},
		{"object", `{"amount": 0.1}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := embeddingsProvider(t, `{
				"object": "list",
				"model": "openai/text-embedding-3-small",
				"data": [],
				"usage": {"prompt_tokens": 1000, "total_tokens": 1000},
				"cost": `+tc.cost+`
			}`)

			resp, err := provider.Embeddings(context.Background(), embeddingRequest())
			if err != nil {
				t.Fatalf("Embeddings() error = %v", err)
			}
			if _, present := resp.Usage.RawUsage["cost"]; present {
				t.Fatalf("RawUsage[cost] = %v, want absent for an unusable cost member", resp.Usage.RawUsage["cost"])
			}

			rate := 0.02
			entry := usage.ExtractFromEmbeddingResponse(resp, "req", providerType, "/v1/embeddings",
				&core.ModelPricing{Currency: "USD", InputPerMtok: &rate})
			if entry.CostSource != usage.CostSourceModelPricing {
				t.Errorf("CostSource = %q, want the token-rate fallback %q", entry.CostSource, usage.CostSourceModelPricing)
			}
		})
	}
}

// TestEmbeddings_ZeroCostIsRecordedAsFree asserts an explicit zero is a real
// price, not a missing one: Eden reporting no charge must be recorded as $0
// rather than silently repriced from the rate card.
func TestEmbeddings_ZeroCostIsRecordedAsFree(t *testing.T) {
	provider := embeddingsProvider(t, `{
		"object": "list",
		"model": "openai/text-embedding-3-small",
		"data": [],
		"usage": {"prompt_tokens": 1000, "total_tokens": 1000},
		"cost": 0
	}`)

	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}

	rate := 0.02
	entry := usage.ExtractFromEmbeddingResponse(resp, "req", providerType, "/v1/embeddings",
		&core.ModelPricing{Currency: "USD", InputPerMtok: &rate})
	if entry.TotalCost == nil || *entry.TotalCost != 0 {
		t.Fatalf("TotalCost = %v, want 0 recorded from Eden's explicit zero", entry.TotalCost)
	}
	if entry.CostSource != usage.CostSourceEdenAICost {
		t.Errorf("CostSource = %q, want %q", entry.CostSource, usage.CostSourceEdenAICost)
	}
}

// TestEmbeddings_UsageLevelCostWins asserts the conventional location stays
// authoritative. If Eden ever also reports cost inside usage, that is the more
// specific reading and the root-level value must not overwrite it.
func TestEmbeddings_UsageLevelCostWins(t *testing.T) {
	provider := embeddingsProvider(t, `{
		"object": "list",
		"model": "openai/text-embedding-3-small",
		"data": [],
		"usage": {"prompt_tokens": 9, "total_tokens": 9, "raw_usage": {"cost": 0.5}},
		"cost": 0.0000012
	}`)

	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if got := resp.Usage.RawUsage["cost"]; got != 0.5 {
		t.Errorf("RawUsage[cost] = %v, want the pre-existing usage-level 0.5 preserved", got)
	}
}

// TestEmbeddings_NilRequestRejected asserts the guard the shared helper used to
// provide is still in place now that the provider issues the request itself.
func TestEmbeddings_NilRequestRejected(t *testing.T) {
	provider := embeddingsProvider(t, edenEmbeddingBody)

	if _, err := provider.Embeddings(context.Background(), nil); err == nil {
		t.Fatal("Embeddings(nil) = nil error, want a rejection")
	}
}

// TestEmbeddings_BackfillsModelFromRequest asserts the EnsureModel behavior the
// shared helper applied is preserved: a response that omits the model is
// labelled with the requested one, so usage rows are attributed correctly.
func TestEmbeddings_BackfillsModelFromRequest(t *testing.T) {
	provider := embeddingsProvider(t, `{
		"object": "list",
		"data": [],
		"usage": {"prompt_tokens": 1, "total_tokens": 1}
	}`)

	resp, err := provider.Embeddings(context.Background(), embeddingRequest())
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != slashedModel {
		t.Errorf("Model = %q, want the requested %q backfilled", resp.Model, slashedModel)
	}
}
