package gateway

import (
	"context"
	"io"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// InferenceConfig configures translated inference orchestration.
type InferenceConfig struct {
	Provider                 core.RoutableProvider
	ModelResolver            ModelResolver
	ModelAuthorizer          ModelAuthorizer
	WorkflowPolicyResolver   WorkflowPolicyResolver
	FallbackResolver         FallbackResolver
	TranslatedRequestPatcher TranslatedRequestPatcher
	UsageLogger              usage.LoggerInterface
	PricingResolver          usage.PricingResolver
	GuardrailsHash           string
}

// InferenceOrchestrator owns translated inference workflow resolution, request
// patching, provider dispatch, fallback, usage logging, and cache metadata.
type InferenceOrchestrator struct {
	provider                 core.RoutableProvider
	modelResolver            ModelResolver
	modelAuthorizer          ModelAuthorizer
	workflowPolicyResolver   WorkflowPolicyResolver
	fallbackResolver         FallbackResolver
	translatedRequestPatcher TranslatedRequestPatcher
	usageLogger              usage.LoggerInterface
	pricingResolver          usage.PricingResolver
	guardrailsHash           string
}

// NewInferenceOrchestrator creates a translated inference orchestrator.
func NewInferenceOrchestrator(cfg InferenceConfig) *InferenceOrchestrator {
	return &InferenceOrchestrator{
		provider:                 cfg.Provider,
		modelResolver:            cfg.ModelResolver,
		modelAuthorizer:          cfg.ModelAuthorizer,
		workflowPolicyResolver:   cfg.WorkflowPolicyResolver,
		fallbackResolver:         cfg.FallbackResolver,
		translatedRequestPatcher: cfg.TranslatedRequestPatcher,
		usageLogger:              cfg.UsageLogger,
		pricingResolver:          cfg.PricingResolver,
		guardrailsHash:           cfg.GuardrailsHash,
	}
}

// RequestMeta carries transport-derived metadata into gateway use cases.
type RequestMeta struct {
	RequestID string
	Endpoint  core.EndpointDescriptor
	Workflow  *core.Workflow
}

// PreparedChatRequest is a translated chat request ready for cache lookup or execution.
type PreparedChatRequest struct {
	Context  context.Context
	Request  *core.ChatRequest
	Workflow *core.Workflow
}

// PreparedResponsesRequest is a translated Responses request ready for cache lookup or execution.
type PreparedResponsesRequest struct {
	Context  context.Context
	Request  *core.ResponsesRequest
	Workflow *core.Workflow
}

// PreparedEmbeddingRequest is a translated embeddings request ready for execution.
type PreparedEmbeddingRequest struct {
	Context  context.Context
	Request  *core.EmbeddingRequest
	Workflow *core.Workflow
}

// ExecutionMeta describes the concrete route used for provider execution.
type ExecutionMeta struct {
	ProviderType  string
	ProviderName  string
	Model         string
	FailoverModel string
	UsedFallback  bool
}

// ChatCompletionResult is the non-streaming chat completion result.
type ChatCompletionResult struct {
	Response *core.ChatResponse
	Meta     ExecutionMeta
}

// ResponsesResult is the non-streaming Responses API result.
type ResponsesResult struct {
	Response *core.ResponsesResponse
	Meta     ExecutionMeta
}

// EmbeddingResult is the embeddings result.
type EmbeddingResult struct {
	Response *core.EmbeddingResponse
	Meta     ExecutionMeta
}

// StreamResult is a provider SSE stream plus route metadata for observers.
type StreamResult struct {
	Stream io.ReadCloser
	Meta   ExecutionMeta
}
