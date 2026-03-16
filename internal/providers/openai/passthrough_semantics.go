package openai

import (
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

type passthroughSemanticEnricher struct{}

func (passthroughSemanticEnricher) ProviderType() string {
	return "openai"
}

func (passthroughSemanticEnricher) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	if info == nil {
		return nil
	}
	enriched := *info
	normalizedEndpoint := strings.TrimLeft(strings.TrimSpace(providers.PassthroughEndpointPath(&enriched)), "/")
	switch "/" + normalizedEndpoint {
	case "/chat/completions":
		enriched.SemanticOperation = "openai.chat_completions"
		enriched.AuditPath = "/v1/chat/completions"
	case "/responses":
		enriched.SemanticOperation = "openai.responses"
		enriched.AuditPath = "/v1/responses"
	case "/embeddings":
		enriched.SemanticOperation = "openai.embeddings"
		enriched.AuditPath = "/v1/embeddings"
	default:
		if strings.TrimSpace(enriched.AuditPath) == "" && normalizedEndpoint != "" {
			enriched.AuditPath = "/p/openai/" + normalizedEndpoint
		}
	}
	return &enriched
}
