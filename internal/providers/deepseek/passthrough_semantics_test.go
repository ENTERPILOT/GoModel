package deepseek

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPassthroughSemanticEnricher_ProviderType(t *testing.T) {
	e := passthroughSemanticEnricher
	if got := e.ProviderType(); got != "deepseek" {
		t.Fatalf("ProviderType() = %q, want deepseek", got)
	}
}

func TestPassthroughSemanticEnricher_NilInfo_ReturnsNil(t *testing.T) {
	e := passthroughSemanticEnricher
	if got := e.Enrich(nil, nil, nil); got != nil {
		t.Fatalf("Enrich(nil) = %v, want nil", got)
	}
}

func TestPassthroughSemanticEnricher_Enrich(t *testing.T) {
	e := passthroughSemanticEnricher

	tests := []struct {
		name               string
		rawEndpoint        string
		normalizedEndpoint string
		wantSemanticOp     string
		wantAuditPath      string
	}{
		{
			name:           "chat completions",
			rawEndpoint:    "/chat/completions",
			wantSemanticOp: "deepseek.chat_completions",
			wantAuditPath:  "/v1/chat/completions",
		},
		{
			name:           "FIM completions",
			rawEndpoint:    "/beta/completions",
			wantSemanticOp: "deepseek.fim_completions",
			wantAuditPath:  "/beta/completions",
		},
		{
			name:          "unknown endpoint gets prefixed audit path",
			rawEndpoint:   "/v1/models",
			wantAuditPath: "/p/deepseek/v1/models",
		},
		{
			name:               "NormalizedEndpoint takes precedence over RawEndpoint",
			rawEndpoint:        "/ignored",
			normalizedEndpoint: "/chat/completions",
			wantSemanticOp:     "deepseek.chat_completions",
			wantAuditPath:      "/v1/chat/completions",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &core.PassthroughRouteInfo{
				RawEndpoint:        tc.rawEndpoint,
				NormalizedEndpoint: tc.normalizedEndpoint,
			}
			got := e.Enrich(nil, nil, info)
			if got == nil {
				t.Fatal("Enrich() returned nil, want enriched info")
			}
			if tc.wantSemanticOp != "" && got.SemanticOperation != tc.wantSemanticOp {
				t.Errorf("SemanticOperation = %q, want %q", got.SemanticOperation, tc.wantSemanticOp)
			}
			if got.AuditPath != tc.wantAuditPath {
				t.Errorf("AuditPath = %q, want %q", got.AuditPath, tc.wantAuditPath)
			}
		})
	}
}
