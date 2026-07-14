package openai

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("openai", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "openai.chat_completions", AuditPath: "/v1/chat/completions"},
	"/responses":        {Operation: "openai.responses", AuditPath: "/v1/responses"},
	"/embeddings":       {Operation: "openai.embeddings", AuditPath: "/v1/embeddings"},
})
