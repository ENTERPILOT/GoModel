package anthropic

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("anthropic", map[string]providers.PassthroughEndpointSemantics{
	"/messages":         {Operation: "anthropic.messages", AuditPath: "/v1/messages"},
	"/messages/batches": {Operation: "anthropic.messages_batches", AuditPath: "/v1/messages/batches"},
})
