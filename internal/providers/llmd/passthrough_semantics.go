package llmd

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("llmd", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "llmd.chat_completions", RequestLogPath: "/v1/chat/completions"},
	"/responses":        {Operation: "llmd.responses", RequestLogPath: "/v1/responses"},
	"/embeddings":       {Operation: "llmd.embeddings", RequestLogPath: "/v1/embeddings"},
	"/completions":      {Operation: "llmd.completions", RequestLogPath: "/v1/completions"},
	"/messages":         {Operation: "llmd.messages", RequestLogPath: "/v1/messages"},
	"/inference/v1/generate": {
		Operation:      "llmd.vllm_generate",
		RequestLogPath: "/inference/v1/generate",
	},
})
