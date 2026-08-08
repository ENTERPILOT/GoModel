package llmclient

// Well-known GenAI operation names carried through observability hooks.
// Provider adapters select these explicitly so instrumentation never has to
// infer semantics from provider-specific URL shapes.
const (
	OperationChat            = "chat"
	OperationGenerateContent = "generate_content"
	OperationTextCompletion  = "text_completion"
	OperationEmbeddings      = "embeddings"
)
