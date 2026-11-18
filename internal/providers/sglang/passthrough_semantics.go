package sglang

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("sglang", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "sglang.chat_completions", GenAIOperation: "chat", RequestLogPath: "/v1/chat/completions"},
	"/responses":        {Operation: "sglang.responses", GenAIOperation: "chat", RequestLogPath: "/v1/responses"},
	"/embeddings":       {Operation: "sglang.embeddings", GenAIOperation: "embeddings", RequestLogPath: "/v1/embeddings"},
	"/completions":      {Operation: "sglang.completions", GenAIOperation: "text_completion", RequestLogPath: "/v1/completions"},
})
