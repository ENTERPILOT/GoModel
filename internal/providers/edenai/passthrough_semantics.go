package edenai

import "github.com/enterpilot/gomodel/internal/providers"

// Eden AI's chat completions and embeddings routes are OpenAI-shaped, so they
// carry OpenAI's semantics and audit paths. Eden's /responses route is
// deliberately absent: it is not the OpenAI Responses API, and labelling it as
// one would attach a /v1/responses audit path to a request whose body follows
// a different contract. Unlisted endpoints keep the generic /p/edenai/...
// audit path from SemanticEnricher.
var passthroughSemanticEnricher = providers.NewSemanticEnricher("edenai", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "edenai.chat_completions", GenAIOperation: "chat", AuditPath: "/v1/chat/completions"},
	"/embeddings":       {Operation: "edenai.embeddings", GenAIOperation: "embeddings", AuditPath: "/v1/embeddings"},
})
