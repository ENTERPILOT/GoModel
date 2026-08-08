package sglang

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("sglang", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "sglang.chat_completions", GenAIOperation: "chat", AuditPath: "/v1/chat/completions"},
	"/responses":        {Operation: "sglang.responses", GenAIOperation: "chat", AuditPath: "/v1/responses"},
	"/embeddings":       {Operation: "sglang.embeddings", GenAIOperation: "embeddings", AuditPath: "/v1/embeddings"},
	"/completions":      {Operation: "sglang.completions", GenAIOperation: "text_completion", AuditPath: "/v1/completions"},
})
