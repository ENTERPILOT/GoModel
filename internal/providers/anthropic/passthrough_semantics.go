package anthropic

import (
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

type passthroughSemanticEnricher struct{}

func (passthroughSemanticEnricher) ProviderType() string {
	return "anthropic"
}

func (passthroughSemanticEnricher) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	if info == nil {
		return nil
	}
	enriched := *info
	normalizedEndpoint := strings.TrimLeft(strings.TrimSpace(providers.PassthroughEndpointPath(&enriched)), "/")
	switch "/" + normalizedEndpoint {
	case "/messages":
		enriched.SemanticOperation = "anthropic.messages"
		enriched.AuditPath = "/v1/messages"
	case "/messages/batches":
		enriched.SemanticOperation = "anthropic.messages_batches"
		enriched.AuditPath = "/v1/messages/batches"
	default:
		if strings.TrimSpace(enriched.AuditPath) == "" && normalizedEndpoint != "" {
			enriched.AuditPath = "/p/anthropic/" + normalizedEndpoint
		}
	}
	return &enriched
}
