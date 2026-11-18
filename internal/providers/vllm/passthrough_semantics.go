package vllm

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("vllm", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "vllm.chat_completions", GenAIOperation: "chat", RequestLogPath: "/v1/chat/completions"},
	"/responses":        {Operation: "vllm.responses", GenAIOperation: "chat", RequestLogPath: "/v1/responses"},
	"/embeddings":       {Operation: "vllm.embeddings", GenAIOperation: "embeddings", RequestLogPath: "/v1/embeddings"},
	"/completions":      {Operation: "vllm.completions", GenAIOperation: "text_completion", RequestLogPath: "/v1/completions"},
})
