package guardrails

import (
	"context"
	"io"

	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
)

// GuardedProvider wraps a RoutableProvider and applies the guardrails pipeline
// before routing requests to providers. It implements core.RoutableProvider.
//
// Adapters convert between concrete request types and the normalized []Message
// DTO that guardrails operate on. This decouples guardrails from API-specific types.
type GuardedProvider struct {
	inner    core.RoutableProvider
	pipeline *Pipeline
	options  Options
}

// Options controls optional behavior of GuardedProvider.
type Options struct {
	EnableForBatchProcessing bool
	// DisableTranslatedRequestProcessing lets an explicit server-side executor own
	// translated-route patching while this wrapper still handles batch rewriting.
	DisableTranslatedRequestProcessing bool
}

// NewGuardedProvider creates a RoutableProvider that applies guardrails
// before delegating to the inner provider.
func NewGuardedProvider(inner core.RoutableProvider, pipeline *Pipeline) *GuardedProvider {
	return NewGuardedProviderWithOptions(inner, pipeline, Options{})
}

// NewGuardedProviderWithOptions creates a RoutableProvider with explicit options.
func NewGuardedProviderWithOptions(inner core.RoutableProvider, pipeline *Pipeline, options Options) *GuardedProvider {
	return &GuardedProvider{
		inner:    inner,
		pipeline: pipeline,
		options:  options,
	}
}

// Supports delegates to the inner provider.
func (g *GuardedProvider) Supports(model string) bool {
	return g.inner.Supports(model)
}

// GetProviderType delegates to the inner provider.
func (g *GuardedProvider) GetProviderType(model string) string {
	return g.inner.GetProviderType(model)
}

// ModelCount delegates to the inner provider when it exposes registry size.
// It returns -1 when the wrapped provider does not expose model count.
func (g *GuardedProvider) ModelCount() int {
	if counted, ok := g.inner.(interface{ ModelCount() int }); ok {
		return counted.ModelCount()
	}
	return -1
}

// NativeFileProviderTypes delegates provider capability inventory to the inner
// provider when available.
func (g *GuardedProvider) NativeFileProviderTypes() []string {
	if typed, ok := g.inner.(core.NativeFileProviderTypeLister); ok {
		return typed.NativeFileProviderTypes()
	}
	return nil
}

// NativeBatchProviderTypes delegates provider capability inventory to the inner
// provider when available.
func (g *GuardedProvider) NativeBatchProviderTypes() []string {
	if typed, ok := g.inner.(core.NativeBatchProviderTypeLister); ok {
		return typed.NativeBatchProviderTypes()
	}
	return nil
}

// NativeResponseProviderTypes delegates provider capability inventory to the
// inner provider when available.
func (g *GuardedProvider) NativeResponseProviderTypes() []string {
	if typed, ok := g.inner.(core.NativeResponseProviderTypeLister); ok {
		return typed.NativeResponseProviderTypes()
	}
	return nil
}

// ChatCompletion extracts messages, applies guardrails, then routes the request.
func (g *GuardedProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if g.options.DisableTranslatedRequestProcessing {
		return g.inner.ChatCompletion(ctx, req)
	}
	modified, err := processGuardedChat(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.ChatCompletion(ctx, modified)
}

// StreamChatCompletion extracts messages, applies guardrails, then routes the streaming request.
func (g *GuardedProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if g.options.DisableTranslatedRequestProcessing {
		return g.inner.StreamChatCompletion(ctx, req)
	}
	modified, err := processGuardedChat(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.StreamChatCompletion(ctx, modified)
}

// ListModels delegates directly to the inner provider (no guardrails needed).
func (g *GuardedProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return g.inner.ListModels(ctx)
}

// Embeddings delegates directly to the inner provider (no guardrails needed for embeddings).
func (g *GuardedProvider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return g.inner.Embeddings(ctx, req)
}

// Responses extracts messages, applies guardrails, then routes the request.
func (g *GuardedProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if g.options.DisableTranslatedRequestProcessing {
		return g.inner.Responses(ctx, req)
	}
	modified, err := processGuardedResponses(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.Responses(ctx, modified)
}

// StreamResponses extracts messages, applies guardrails, then routes the streaming request.
func (g *GuardedProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if g.options.DisableTranslatedRequestProcessing {
		return g.inner.StreamResponses(ctx, req)
	}
	modified, err := processGuardedResponses(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.StreamResponses(ctx, modified)
}

func (g *GuardedProvider) nativeBatchRouter() (core.NativeBatchRoutableProvider, error) {
	bp, ok := g.inner.(core.NativeBatchRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("batch routing is not supported by the current provider router", nil)
	}
	return bp, nil
}

func (g *GuardedProvider) nativeBatchHintRouter() (core.NativeBatchHintRoutableProvider, error) {
	hinted, ok := g.inner.(core.NativeBatchHintRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("batch hint routing is not supported by the current provider router", nil)
	}
	return hinted, nil
}

func (g *GuardedProvider) nativeFileRouter() (core.NativeFileRoutableProvider, error) {
	fp, ok := g.inner.(core.NativeFileRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("file routing is not supported by the current provider router", nil)
	}
	return fp, nil
}

func (g *GuardedProvider) nativeResponseLifecycleRouter() (core.NativeResponseLifecycleRoutableProvider, error) {
	responses, ok := g.inner.(core.NativeResponseLifecycleRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("response lifecycle routing is not supported by the current provider router", nil)
	}
	return responses, nil
}

func (g *GuardedProvider) nativeResponseUtilityRouter() (core.NativeResponseUtilityRoutableProvider, error) {
	responses, ok := g.inner.(core.NativeResponseUtilityRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("response utility routing is not supported by the current provider router", nil)
	}
	return responses, nil
}

func (g *GuardedProvider) batchFileTransport() core.BatchFileTransport {
	files, err := g.nativeFileRouter()
	if err != nil {
		return nil
	}
	return files
}

func (g *GuardedProvider) passthroughRouter() (core.RoutablePassthrough, error) {
	pp, ok := g.inner.(core.RoutablePassthrough)
	if !ok {
		return nil, core.NewInvalidRequestError("passthrough routing is not supported by the current provider router", nil)
	}
	return pp, nil
}

// CreateBatch delegates native batch creation and optionally applies guardrails to inline items.
func (g *GuardedProvider) CreateBatch(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, error) {
	bp, err := g.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	if !g.options.EnableForBatchProcessing {
		return bp.CreateBatch(ctx, providerType, req)
	}

	result, err := processGuardedBatchRequest(ctx, providerType, req, g.pipeline, g.batchFileTransport())
	if err != nil {
		return nil, err
	}
	batchrewrite.RecordPreparation(ctx, req, result.Request)
	resp, err := bp.CreateBatch(ctx, providerType, result.Request)
	if err != nil {
		batchrewrite.CleanupFileFromRouter(ctx, g.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
		return nil, err
	}
	batchrewrite.CleanupSupersededFileFromRouter(ctx, g.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
	return resp, nil
}

// CreateBatchWithHints delegates hint-aware native batch creation while preserving
// guardrail batch processing when enabled.
func (g *GuardedProvider) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	hinted, err := g.nativeBatchHintRouter()
	if err != nil {
		return nil, nil, err
	}
	if !g.options.EnableForBatchProcessing {
		return hinted.CreateBatchWithHints(ctx, providerType, req)
	}

	result, err := processGuardedBatchRequest(ctx, providerType, req, g.pipeline, g.batchFileTransport())
	if err != nil {
		return nil, nil, err
	}
	batchrewrite.RecordPreparation(ctx, req, result.Request)
	resp, hints, err := hinted.CreateBatchWithHints(ctx, providerType, result.Request)
	if err != nil {
		batchrewrite.CleanupFileFromRouter(ctx, g.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
		return nil, nil, err
	}
	batchrewrite.CleanupSupersededFileFromRouter(ctx, g.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
	return resp, batchrewrite.MergeEndpointHints(result.RequestEndpointHints, hints), nil
}

// GetBatch delegates native batch retrieval.
func (g *GuardedProvider) GetBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	bp, err := g.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return bp.GetBatch(ctx, providerType, id)
}

// ListBatches delegates native batch listing.
func (g *GuardedProvider) ListBatches(ctx context.Context, providerType string, limit int, after string) (*core.BatchListResponse, error) {
	bp, err := g.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return bp.ListBatches(ctx, providerType, limit, after)
}

// CancelBatch delegates native batch cancellation.
func (g *GuardedProvider) CancelBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	bp, err := g.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return bp.CancelBatch(ctx, providerType, id)
}

// GetBatchResults delegates native batch results retrieval.
func (g *GuardedProvider) GetBatchResults(ctx context.Context, providerType, id string) (*core.BatchResultsResponse, error) {
	bp, err := g.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return bp.GetBatchResults(ctx, providerType, id)
}

// GetBatchResultsWithHints delegates hint-aware native batch results retrieval.
func (g *GuardedProvider) GetBatchResultsWithHints(ctx context.Context, providerType, id string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	hinted, err := g.nativeBatchHintRouter()
	if err != nil {
		return nil, err
	}
	return hinted.GetBatchResultsWithHints(ctx, providerType, id, endpointByCustomID)
}

// ClearBatchResultHints delegates cleanup of transient provider-side result hints.
func (g *GuardedProvider) ClearBatchResultHints(providerType, batchID string) {
	hinted, err := g.nativeBatchHintRouter()
	if err != nil {
		return
	}
	hinted.ClearBatchResultHints(providerType, batchID)
}

// CreateFile delegates native file upload.
func (g *GuardedProvider) CreateFile(ctx context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	fp, err := g.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return fp.CreateFile(ctx, providerType, req)
}

// ListFiles delegates native file listing.
func (g *GuardedProvider) ListFiles(ctx context.Context, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	fp, err := g.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return fp.ListFiles(ctx, providerType, purpose, limit, after)
}

// GetFile delegates native file lookup.
func (g *GuardedProvider) GetFile(ctx context.Context, providerType, id string) (*core.FileObject, error) {
	fp, err := g.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return fp.GetFile(ctx, providerType, id)
}

// DeleteFile delegates native file deletion.
func (g *GuardedProvider) DeleteFile(ctx context.Context, providerType, id string) (*core.FileDeleteResponse, error) {
	fp, err := g.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return fp.DeleteFile(ctx, providerType, id)
}

// GetFileContent delegates native file content retrieval.
func (g *GuardedProvider) GetFileContent(ctx context.Context, providerType, id string) (*core.FileContentResponse, error) {
	fp, err := g.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return fp.GetFileContent(ctx, providerType, id)
}

// Passthrough delegates opaque provider-native requests without semantic guardrail processing.
func (g *GuardedProvider) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	pp, err := g.passthroughRouter()
	if err != nil {
		return nil, err
	}
	return pp.Passthrough(ctx, providerType, req)
}

// GetResponse delegates native response lookup.
func (g *GuardedProvider) GetResponse(ctx context.Context, providerType, id string, params core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	responses, err := g.nativeResponseLifecycleRouter()
	if err != nil {
		return nil, err
	}
	return responses.GetResponse(ctx, providerType, id, params)
}

// ListResponseInputItems delegates native response input item listing.
func (g *GuardedProvider) ListResponseInputItems(ctx context.Context, providerType, id string, params core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	responses, err := g.nativeResponseLifecycleRouter()
	if err != nil {
		return nil, err
	}
	return responses.ListResponseInputItems(ctx, providerType, id, params)
}

// CancelResponse delegates native response cancellation.
func (g *GuardedProvider) CancelResponse(ctx context.Context, providerType, id string) (*core.ResponsesResponse, error) {
	responses, err := g.nativeResponseLifecycleRouter()
	if err != nil {
		return nil, err
	}
	return responses.CancelResponse(ctx, providerType, id)
}

// DeleteResponse delegates native response deletion.
func (g *GuardedProvider) DeleteResponse(ctx context.Context, providerType, id string) (*core.ResponseDeleteResponse, error) {
	responses, err := g.nativeResponseLifecycleRouter()
	if err != nil {
		return nil, err
	}
	return responses.DeleteResponse(ctx, providerType, id)
}

// CountResponseInputTokens delegates native response token counting.
func (g *GuardedProvider) CountResponseInputTokens(ctx context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseInputTokensResponse, error) {
	responses, err := g.nativeResponseUtilityRouter()
	if err != nil {
		return nil, err
	}
	return responses.CountResponseInputTokens(ctx, providerType, req)
}

// CompactResponse delegates native response compaction.
func (g *GuardedProvider) CompactResponse(ctx context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseCompactResponse, error) {
	responses, err := g.nativeResponseUtilityRouter()
	if err != nil {
		return nil, err
	}
	return responses.CompactResponse(ctx, providerType, req)
}

// PatchChatRequest applies guardrails to a translated chat request without
// delegating to the wrapped provider.
func (g *GuardedProvider) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return processGuardedChat(ctx, g.pipeline, req)
}

// PatchResponsesRequest applies guardrails to a translated responses request
// without delegating to the wrapped provider.
func (g *GuardedProvider) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return processGuardedResponses(ctx, g.pipeline, req)
}

// PrepareBatchRequest applies guardrails to batch subrequests without
// submitting the native batch to the wrapped provider.
func (g *GuardedProvider) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	if !g.options.EnableForBatchProcessing {
		return &core.BatchRewriteResult{Request: req}, nil
	}
	return processGuardedBatchRequest(ctx, providerType, req, g.pipeline, g.batchFileTransport())
}

func cloneToolCalls(toolCalls []core.ToolCall) []core.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	cloned := make([]core.ToolCall, len(toolCalls))
	for i, toolCall := range toolCalls {
		cloned[i] = core.ToolCall{
			ID:   toolCall.ID,
			Type: toolCall.Type,
			Function: core.FunctionCall{
				Name:        toolCall.Function.Name,
				Arguments:   toolCall.Function.Arguments,
				ExtraFields: core.CloneUnknownJSONFields(toolCall.Function.ExtraFields),
			},
			ExtraFields: core.CloneUnknownJSONFields(toolCall.ExtraFields),
		}
	}
	return cloned
}

func cloneChatMessageEnvelope(message core.Message) core.Message {
	return core.Message{
		Role:        message.Role,
		ToolCallID:  message.ToolCallID,
		ContentNull: message.ContentNull,
		Content:     cloneMessageContent(message.Content),
		ToolCalls:   cloneToolCalls(message.ToolCalls),
		ExtraFields: core.CloneUnknownJSONFields(message.ExtraFields),
	}
}

func cloneMessageContent(content any) any {
	switch value := content.(type) {
	case nil:
		return nil
	case string:
		return value
	case []core.ContentPart:
		return cloneContentParts(value)
	default:
		parts, ok := core.NormalizeContentParts(content)
		if !ok {
			return value
		}
		return cloneContentParts(parts)
	}
}

func cloneContentParts(parts []core.ContentPart) []core.ContentPart {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]core.ContentPart, len(parts))
	for i, part := range parts {
		cloned[i] = cloneContentPart(part)
	}
	return cloned
}

func cloneContentPart(part core.ContentPart) core.ContentPart {
	cloned := core.ContentPart{
		Type:        part.Type,
		Text:        part.Text,
		ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
	}
	if part.ImageURL != nil {
		cloned.ImageURL = &core.ImageURLContent{
			URL:         part.ImageURL.URL,
			Detail:      part.ImageURL.Detail,
			MediaType:   part.ImageURL.MediaType,
			ExtraFields: core.CloneUnknownJSONFields(part.ImageURL.ExtraFields),
		}
	}
	if part.InputAudio != nil {
		cloned.InputAudio = &core.InputAudioContent{
			Data:        part.InputAudio.Data,
			Format:      part.InputAudio.Format,
			ExtraFields: core.CloneUnknownJSONFields(part.InputAudio.ExtraFields),
		}
	}
	return cloned
}
