// Package core defines the core interfaces and types for the LLM gateway.
package core

import (
	"context"
	"io"
)

// Provider defines the interface for LLM providers
type Provider interface {
	// ChatCompletion executes a chat completion request
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)

	// StreamChatCompletion returns a raw SSE stream (caller must close)
	StreamChatCompletion(ctx context.Context, req *ChatRequest) (io.ReadCloser, error)

	// ListModels returns the list of available models
	ListModels(ctx context.Context) (*ModelsResponse, error)

	// Responses executes a Responses API request (OpenAI-compatible)
	Responses(ctx context.Context, req *ResponsesRequest) (*ResponsesResponse, error)

	// StreamResponses returns a raw SSE stream for Responses API (caller must close)
	StreamResponses(ctx context.Context, req *ResponsesRequest) (io.ReadCloser, error)

	// Embeddings sends an embeddings request to the provider
	Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)
}

// NativeBatchProvider is implemented by providers that support native discounted batching.
// This is intentionally separate from Provider so unsupported providers can still implement
// regular synchronous APIs without batch capabilities.
type NativeBatchProvider interface {
	CreateBatch(ctx context.Context, req *BatchRequest) (*BatchResponse, error)
	GetBatch(ctx context.Context, id string) (*BatchResponse, error)
	ListBatches(ctx context.Context, limit int, after string) (*BatchListResponse, error)
	CancelBatch(ctx context.Context, id string) (*BatchResponse, error)
	GetBatchResults(ctx context.Context, id string) (*BatchResultsResponse, error)
}

// BatchCreateHintAwareProvider is an optional native batch extension for
// providers that need gateway persistence for per-item endpoint hints.
type BatchCreateHintAwareProvider interface {
	CreateBatchWithHints(ctx context.Context, req *BatchRequest) (*BatchResponse, map[string]string, error)
}

// BatchResultHintAwareProvider is an optional native batch extension for
// providers that need persisted per-item endpoint hints to shape results.
type BatchResultHintAwareProvider interface {
	GetBatchResultsWithHints(ctx context.Context, id string, endpointByCustomID map[string]string) (*BatchResultsResponse, error)
	ClearBatchResultHints(batchID string)
}

// NativeBatchRoutableProvider extends routing with native batch operations.
type NativeBatchRoutableProvider interface {
	CreateBatch(ctx context.Context, providerType string, req *BatchRequest) (*BatchResponse, error)
	GetBatch(ctx context.Context, providerType, id string) (*BatchResponse, error)
	ListBatches(ctx context.Context, providerType string, limit int, after string) (*BatchListResponse, error)
	CancelBatch(ctx context.Context, providerType, id string) (*BatchResponse, error)
	GetBatchResults(ctx context.Context, providerType, id string) (*BatchResultsResponse, error)
}

// NativeBatchHintRoutableProvider is an optional routing extension for
// providers that can consume persisted per-item endpoint hints.
type NativeBatchHintRoutableProvider interface {
	CreateBatchWithHints(ctx context.Context, providerType string, req *BatchRequest) (*BatchResponse, map[string]string, error)
	GetBatchResultsWithHints(ctx context.Context, providerType, id string, endpointByCustomID map[string]string) (*BatchResultsResponse, error)
	ClearBatchResultHints(providerType, batchID string)
}

// NativeFileProvider is implemented by providers that support OpenAI-compatible files APIs.
type NativeFileProvider interface {
	CreateFile(ctx context.Context, req *FileCreateRequest) (*FileObject, error)
	ListFiles(ctx context.Context, purpose string, limit int, after string) (*FileListResponse, error)
	GetFile(ctx context.Context, id string) (*FileObject, error)
	DeleteFile(ctx context.Context, id string) (*FileDeleteResponse, error)
	GetFileContent(ctx context.Context, id string) (*FileContentResponse, error)
}

// NativeFileRoutableProvider extends routing with provider-native file operations.
type NativeFileRoutableProvider interface {
	CreateFile(ctx context.Context, providerType string, req *FileCreateRequest) (*FileObject, error)
	ListFiles(ctx context.Context, providerType, purpose string, limit int, after string) (*FileListResponse, error)
	GetFile(ctx context.Context, providerType, id string) (*FileObject, error)
	DeleteFile(ctx context.Context, providerType, id string) (*FileDeleteResponse, error)
	GetFileContent(ctx context.Context, providerType, id string) (*FileContentResponse, error)
}

// NativeFileProviderTypeLister exposes registered provider types that support
// native file operations. This is an internal capability inventory and must not
// depend on the public model catalog.
type NativeFileProviderTypeLister interface {
	NativeFileProviderTypes() []string
}

// RoutableProvider extends Provider with routing capability.
// This is implemented by the Router which uses a model registry
// to determine if a model is supported.
type RoutableProvider interface {
	Provider

	Supports(model string) bool
	GetProviderType(model string) string
}

// ProviderNameResolver is an optional interface for components that can map a
// routed model selector back to the concrete configured provider instance name.
type ProviderNameResolver interface {
	GetProviderName(model string) string
}

// AvailabilityChecker is an optional interface for providers that need
// to verify service availability before registration.
type AvailabilityChecker interface {
	// CheckAvailability verifies the provider's backend service is reachable.
	// Returns nil if available, error otherwise.
	CheckAvailability(ctx context.Context) error
}

// ModelLookup defines the interface for looking up models and their providers.
// This abstraction allows the Router to be decoupled from the concrete ModelRegistry implementation.
type ModelLookup interface {
	// Supports returns true if the registry has a provider for the given model
	Supports(model string) bool

	// GetProvider returns the provider for the given model, or nil if not found
	GetProvider(model string) Provider

	// GetProviderType returns the provider type string for the given model.
	// Returns empty string if the model is not found.
	GetProviderType(model string) string

	// ListModels returns all models in the registry
	ListModels() []Model

	// ModelCount returns the number of registered models
	ModelCount() int
}
