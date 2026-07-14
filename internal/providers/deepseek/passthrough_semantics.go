package deepseek

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("deepseek", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "deepseek.chat_completions", AuditPath: "/v1/chat/completions"},
	"/beta/completions": {Operation: "deepseek.fim_completions", AuditPath: "/beta/completions"},
})
