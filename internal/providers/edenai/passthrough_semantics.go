package edenai

import "github.com/enterpilot/gomodel/internal/providers"

// Eden AI's chat completions and embeddings routes are OpenAI-shaped, so they
// carry OpenAI's semantics and request log paths. Eden's /responses route is
// deliberately absent: it is not the OpenAI Responses API, and labelling it as
// one would attach a /v1/responses request log path to a request whose body follows
// a different contract. Unlisted endpoints keep the generic /p/edenai/...
// request log path from SemanticEnricher.
var passthroughSemanticEnricher = providers.NewSemanticEnricher("edenai", map[string]providers.PassthroughEndpointSemantics{
	"/chat/completions": {Operation: "edenai.chat_completions", GenAIOperation: "chat", RequestLogPath: "/v1/chat/completions"},
	"/embeddings":       {Operation: "edenai.embeddings", GenAIOperation: "embeddings", RequestLogPath: "/v1/embeddings"},
})
