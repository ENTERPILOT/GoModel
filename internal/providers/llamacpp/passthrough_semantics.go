package llamacpp

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("llamacpp", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "llamacpp.chat_completions", GenAIOperation: "chat", RequestLogPath: "/v1/chat/completions"},
	"/responses":        {Operation: "llamacpp.responses", GenAIOperation: "chat", RequestLogPath: "/v1/responses"},
	"/embeddings":       {Operation: "llamacpp.embeddings", GenAIOperation: "embeddings", RequestLogPath: "/v1/embeddings"},
	"/completions":      {Operation: "llamacpp.completions", GenAIOperation: "text_completion", RequestLogPath: "/v1/completions"},
})
