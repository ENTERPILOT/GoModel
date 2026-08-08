package llmd

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("llmd", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "llmd.chat_completions", AuditPath: "/v1/chat/completions"},
	"/responses":        {Operation: "llmd.responses", AuditPath: "/v1/responses"},
	"/embeddings":       {Operation: "llmd.embeddings", AuditPath: "/v1/embeddings"},
	"/completions":      {Operation: "llmd.completions", AuditPath: "/v1/completions"},
	"/messages":         {Operation: "llmd.messages", AuditPath: "/v1/messages"},
	"/inference/v1/generate": {
		Operation: "llmd.vllm_generate",
		AuditPath: "/inference/v1/generate",
	},
})
