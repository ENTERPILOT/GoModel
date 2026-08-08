package sglang

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("sglang", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "sglang.chat_completions", AuditPath: "/v1/chat/completions"},
	"/responses":        {Operation: "sglang.responses", AuditPath: "/v1/responses"},
	"/embeddings":       {Operation: "sglang.embeddings", AuditPath: "/v1/embeddings"},
	"/completions":      {Operation: "sglang.completions", AuditPath: "/v1/completions"},
})
