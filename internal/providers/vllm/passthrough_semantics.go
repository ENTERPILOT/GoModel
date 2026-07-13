package vllm

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("vllm", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "vllm.chat_completions", AuditPath: "/v1/chat/completions"},
	"/responses":        {Operation: "vllm.responses", AuditPath: "/v1/responses"},
	"/embeddings":       {Operation: "vllm.embeddings", AuditPath: "/v1/embeddings"},
	"/completions":      {Operation: "vllm.completions", AuditPath: "/v1/completions"},
})
