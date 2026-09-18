package edenai

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPassthroughSemanticEnricher(t *testing.T) {
	if got := passthroughSemanticEnricher.ProviderType(); got != "edenai" {
		t.Fatalf("ProviderType() = %q, want edenai", got)
	}

	tests := []struct {
		name               string
		rawEndpoint        string
		normalizedEndpoint string
		wantOperation      string
		wantGenAIOperation string
		wantAuditPath      string
	}{
		{
			name:               "chat completions",
			rawEndpoint:        "v1/chat/completions",
			normalizedEndpoint: "chat/completions",
			wantOperation:      "edenai.chat_completions",
			wantGenAIOperation: "chat",
			wantAuditPath:      "/v1/chat/completions",
		},
		{
			name:               "embeddings",
			rawEndpoint:        "v1/embeddings",
			normalizedEndpoint: "embeddings",
			wantOperation:      "edenai.embeddings",
			wantGenAIOperation: "embeddings",
			wantAuditPath:      "/v1/embeddings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
				RawEndpoint:        tt.rawEndpoint,
				NormalizedEndpoint: tt.normalizedEndpoint,
			})
			if got == nil {
				t.Fatal("Enrich() returned nil")
			}
			if got.SemanticOperation != tt.wantOperation {
				t.Errorf("SemanticOperation = %q, want %q", got.SemanticOperation, tt.wantOperation)
			}
			if got.GenAIOperation != tt.wantGenAIOperation {
				t.Errorf("GenAIOperation = %q, want %q", got.GenAIOperation, tt.wantGenAIOperation)
			}
			if got.AuditPath != tt.wantAuditPath {
				t.Errorf("AuditPath = %q, want %q", got.AuditPath, tt.wantAuditPath)
			}
		})
	}
}

// TestPassthroughSemanticEnricher_ResponsesIsNotOpenAIShaped asserts Eden's
// native /responses route is deliberately absent from the table. Eden's
// /v3/responses is not the OpenAI Responses API, so labelling it
// "edenai.responses" with a /v1/responses audit path would record a request as
// something it is not. It must fall through to the generic /p/edenai/... path.
func TestPassthroughSemanticEnricher_ResponsesIsNotOpenAIShaped(t *testing.T) {
	got := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		RawEndpoint:        "v1/responses",
		NormalizedEndpoint: "responses",
	})
	if got == nil {
		t.Fatal("Enrich() returned nil")
	}
	if got.SemanticOperation != "" {
		t.Errorf("SemanticOperation = %q, want empty: Eden /responses must not be advertised as OpenAI Responses", got.SemanticOperation)
	}
	if got.AuditPath != "/p/edenai/responses" {
		t.Errorf("AuditPath = %q, want /p/edenai/responses", got.AuditPath)
	}
}
