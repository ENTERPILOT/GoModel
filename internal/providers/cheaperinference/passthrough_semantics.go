package cheaperinference

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewSemanticEnricher("cheaperinference", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "cheaperinference.chat_completions", GenAIOperation: "chat", AuditPath: "/v1/chat/completions"},
})
