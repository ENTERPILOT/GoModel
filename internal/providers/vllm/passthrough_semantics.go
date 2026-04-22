package vllm

import (
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

type passthroughSemanticEnricher struct{}

func (passthroughSemanticEnricher) ProviderType() string {
	return "vllm"
}

func (passthroughSemanticEnricher) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	if info == nil {
		return nil
	}
	enriched := *info
	normalizedEndpoint := strings.TrimLeft(strings.TrimSpace(providers.PassthroughEndpointPath(&enriched)), "/")
	switch "/" + normalizedEndpoint {
	case "/chat/completions":
		enriched.SemanticOperation = "vllm.chat_completions"
		enriched.AuditPath = "/v1/chat/completions"
	case "/responses":
		enriched.SemanticOperation = "vllm.responses"
		enriched.AuditPath = "/v1/responses"
	case "/embeddings":
		enriched.SemanticOperation = "vllm.embeddings"
		enriched.AuditPath = "/v1/embeddings"
	case "/completions":
		enriched.SemanticOperation = "vllm.completions"
		enriched.AuditPath = "/v1/completions"
	default:
		if strings.TrimSpace(enriched.AuditPath) == "" && normalizedEndpoint != "" {
			enriched.AuditPath = "/p/vllm/" + normalizedEndpoint
		}
	}
	return &enriched
}
