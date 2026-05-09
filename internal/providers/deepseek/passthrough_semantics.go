package deepseek

import (
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

type passthroughSemanticEnricher struct{}

func (passthroughSemanticEnricher) ProviderType() string {
	return "deepseek"
}

func (passthroughSemanticEnricher) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	if info == nil {
		return nil
	}
	enriched := *info
	normalizedEndpoint := strings.TrimLeft(strings.TrimSpace(providers.PassthroughEndpointPath(&enriched)), "/")
	switch "/" + normalizedEndpoint {
	case "/chat/completions":
		enriched.SemanticOperation = "deepseek.chat_completions"
		enriched.AuditPath = "/v1/chat/completions"
	case "/beta/completions":
		enriched.SemanticOperation = "deepseek.fim_completions"
		enriched.AuditPath = "/beta/completions"
	default:
		if strings.TrimSpace(enriched.AuditPath) == "" && normalizedEndpoint != "" {
			enriched.AuditPath = "/p/deepseek/" + normalizedEndpoint
		}
	}
	return &enriched
}
