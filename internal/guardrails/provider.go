package guardrails

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"strings"

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

func rewriteGuardedChatBatchBody(originalBody json.RawMessage, original *core.ChatRequest, modified *core.ChatRequest) (json.RawMessage, error) {
	body, err := patchGuardedChatBatchBody(originalBody, original, modified)
	if err == nil {
		return body, nil
	}
	return json.Marshal(modified)
}

func patchGuardedChatBatchBody(originalBody json.RawMessage, original *core.ChatRequest, modified *core.ChatRequest) (json.RawMessage, error) {
	if modified == nil {
		return nil, core.NewInvalidRequestError("missing guarded chat request", nil)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(originalBody, &raw); err != nil {
		return nil, err
	}

	patchedMessages, err := patchChatMessagesJSON(raw["messages"], original.Messages, modified.Messages)
	if err != nil {
		return nil, err
	}
	raw["messages"] = patchedMessages
	return json.Marshal(raw)
}

func patchChatMessagesJSON(originalRaw json.RawMessage, original, modified []core.Message) (json.RawMessage, error) {
	originalRawItems, err := unmarshalJSONArray(originalRaw)
	if err != nil {
		return nil, err
	}
	if len(originalRawItems) != len(original) {
		return nil, core.NewInvalidRequestError("guardrails chat message payload does not match parsed request", nil)
	}

	systemOriginals := make([]json.RawMessage, 0, len(original))
	nonSystemOriginals := make([]json.RawMessage, 0, len(original))
	nonSystemMessages := make([]core.Message, 0, len(original))
	for i, msg := range original {
		if msg.Role == "system" {
			systemOriginals = append(systemOriginals, originalRawItems[i])
			continue
		}
		nonSystemOriginals = append(nonSystemOriginals, originalRawItems[i])
		nonSystemMessages = append(nonSystemMessages, msg)
	}

	patched := make([]json.RawMessage, 0, len(modified))
	modifiedSystemCount := 0
	for _, msg := range modified {
		if msg.Role == "system" {
			modifiedSystemCount++
		}
	}
	systemMatchStart, originalSystemStart := tailMatchedSystemOffsets(len(systemOriginals), modifiedSystemCount)
	nextSystem := 0
	nextNonSystem := 0
	for _, msg := range modified {
		if msg.Role == "system" {
			if nextSystem >= systemMatchStart {
				item, err := patchRawChatMessage(systemOriginals[originalSystemStart+(nextSystem-systemMatchStart)], msg)
				if err != nil {
					return nil, err
				}
				patched = append(patched, item)
			} else {
				item, err := json.Marshal(msg)
				if err != nil {
					return nil, err
				}
				patched = append(patched, item)
			}
			nextSystem++
			continue
		}

		if nextNonSystem >= len(nonSystemOriginals) {
			return nil, core.NewInvalidRequestError("guardrails cannot insert non-system chat messages", nil)
		}
		if nonSystemMessages[nextNonSystem].Role != msg.Role {
			return nil, core.NewInvalidRequestError("guardrails cannot reorder non-system chat messages", nil)
		}
		item, err := patchRawChatMessage(nonSystemOriginals[nextNonSystem], msg)
		if err != nil {
			return nil, err
		}
		patched = append(patched, item)
		nextNonSystem++
	}
	if nextNonSystem != len(nonSystemOriginals) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove non-system chat messages", nil)
	}

	return json.Marshal(patched)
}

func patchRawChatMessage(original json.RawMessage, modified core.Message) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return nil, err
	}

	updatedBody, err := json.Marshal(modified)
	if err != nil {
		return nil, err
	}

	var updated map[string]json.RawMessage
	if err := json.Unmarshal(updatedBody, &updated); err != nil {
		return nil, err
	}

	for _, field := range []string{"role", "content", "tool_calls", "tool_call_id"} {
		delete(raw, field)
		if value, ok := updated[field]; ok {
			raw[field] = value
		}
	}

	return json.Marshal(raw)
}

func rewriteGuardedResponsesBatchBody(originalBody json.RawMessage, modified *core.ResponsesRequest) (json.RawMessage, error) {
	if modified == nil {
		return nil, core.NewInvalidRequestError("missing guarded responses request", nil)
	}

	body, err := patchJSONObjectFields(originalBody, map[string]jsonFieldPatch{
		"instructions": {value: modified.Instructions, omitWhenEmpty: modified.Instructions == ""},
		"input":        {value: modified.Input},
	})
	if err == nil {
		return body, nil
	}
	return json.Marshal(modified)
}

type jsonFieldPatch struct {
	value         any
	omitWhenEmpty bool
}

func patchJSONObjectFields(originalBody json.RawMessage, patches map[string]jsonFieldPatch) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(originalBody, &raw); err != nil {
		return nil, err
	}

	for field, patch := range patches {
		if patch.omitWhenEmpty && isZeroJSONFieldValue(patch.value) {
			delete(raw, field)
			continue
		}

		encoded, err := json.Marshal(patch.value)
		if err != nil {
			return nil, err
		}
		raw[field] = encoded
	}

	return json.Marshal(raw)
}

func unmarshalJSONArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func isZeroJSONFieldValue(value any) bool {
	switch v := value.(type) {
	case string:
		return v == ""
	default:
		return value == nil
	}
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
	g.recordBatchPreparation(ctx, req, result.Request)
	resp, err := bp.CreateBatch(ctx, providerType, result.Request)
	if err != nil {
		g.cleanupBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
		return nil, err
	}
	g.cleanupSupersededBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
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
	g.recordBatchPreparation(ctx, req, result.Request)
	resp, hints, err := hinted.CreateBatchWithHints(ctx, providerType, result.Request)
	if err != nil {
		g.cleanupBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
		return nil, nil, err
	}
	g.cleanupSupersededBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
	return resp, mergeBatchHints(result.RequestEndpointHints, hints), nil
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

func (g *GuardedProvider) recordBatchPreparation(ctx context.Context, original, rewritten *core.BatchRequest) {
	if ctx == nil || original == nil || rewritten == nil {
		return
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return
	}
	metadata.RecordInputFileRewrite(original.InputFileID, rewritten.InputFileID)
}

func (g *GuardedProvider) cleanupSupersededBatchRewriteFile(ctx context.Context, providerType, localRewrittenFileID string) {
	localRewrittenFileID = strings.TrimSpace(localRewrittenFileID)
	if localRewrittenFileID == "" {
		return
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return
	}
	if strings.TrimSpace(metadata.RewrittenInputFileID) == localRewrittenFileID {
		return
	}
	g.cleanupBatchRewriteFile(ctx, providerType, localRewrittenFileID)
}

func (g *GuardedProvider) cleanupBatchRewriteFile(ctx context.Context, providerType, fileID string) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return
	}
	files, err := g.nativeFileRouter()
	if err != nil {
		return
	}
	if _, err := files.DeleteFile(ctx, providerType, fileID); err != nil {
		slog.Warn("failed to delete rewritten batch input file", "provider", providerType, "file_id", fileID, "error", err)
	}
}

func mergeBatchHints(left, right map[string]string) map[string]string {
	if len(left) == 0 {
		if len(right) == 0 {
			return nil
		}
		merged := make(map[string]string, len(right))
		maps.Copy(merged, right)
		return merged
	}
	merged := make(map[string]string, len(left))
	maps.Copy(merged, left)
	maps.Copy(merged, right)
	return merged
}

// Passthrough delegates opaque provider-native requests without semantic guardrail processing.
func (g *GuardedProvider) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	pp, err := g.passthroughRouter()
	if err != nil {
		return nil, err
	}
	return pp.Passthrough(ctx, providerType, req)
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

// --- Adapters: concrete requests ↔ normalized []Message ---

// chatToMessages extracts the normalized message list from a ChatRequest.
func chatToMessages(req *core.ChatRequest) ([]Message, error) {
	msgs := make([]Message, len(req.Messages))
	for i, m := range req.Messages {
		text, err := normalizeGuardrailMessageText(m.Content)
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid chat message content", err)
		}
		msgs[i] = Message{
			Role:        m.Role,
			Content:     text,
			ToolCalls:   cloneToolCalls(m.ToolCalls),
			ToolCallID:  m.ToolCallID,
			ContentNull: m.ContentNull || m.Content == nil,
		}
	}
	return msgs, nil
}

// applyMessagesToChatPreservingEnvelope applies guardrail message updates while
// preserving the original chat message envelopes and structured content shapes.
func applyMessagesToChatPreservingEnvelope(req *core.ChatRequest, msgs []Message) (*core.ChatRequest, error) {
	systemOriginal := make([]core.Message, 0, len(req.Messages))
	nonSystemOriginal := make([]core.Message, 0, len(req.Messages))
	for _, original := range req.Messages {
		if original.Role == "system" {
			systemOriginal = append(systemOriginal, original)
			continue
		}
		nonSystemOriginal = append(nonSystemOriginal, original)
	}

	coreMessages := make([]core.Message, 0, len(msgs))
	modifiedSystemCount := 0
	for _, modified := range msgs {
		if modified.Role == "system" {
			modifiedSystemCount++
		}
	}
	systemMatchStart, originalSystemStart := tailMatchedSystemOffsets(len(systemOriginal), modifiedSystemCount)
	nextSystem := 0
	nextNonSystem := 0
	for _, modified := range msgs {
		if modified.Role == "system" {
			if nextSystem >= systemMatchStart {
				preserved, err := applyGuardedMessageToOriginal(systemOriginal[originalSystemStart+(nextSystem-systemMatchStart)], modified)
				if err != nil {
					return nil, err
				}
				coreMessages = append(coreMessages, preserved)
			} else {
				coreMessages = append(coreMessages, newChatMessageFromGuardrail(modified))
			}
			nextSystem++
			continue
		}

		if nextNonSystem >= len(nonSystemOriginal) {
			return nil, core.NewInvalidRequestError("guardrails cannot insert non-system chat messages", nil)
		}
		original := nonSystemOriginal[nextNonSystem]
		if modified.Role != original.Role {
			return nil, core.NewInvalidRequestError("guardrails cannot reorder non-system chat messages", nil)
		}
		preserved, err := applyGuardedMessageToOriginal(original, modified)
		if err != nil {
			return nil, err
		}
		coreMessages = append(coreMessages, preserved)
		nextNonSystem++
	}

	if nextNonSystem != len(nonSystemOriginal) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove non-system chat messages", nil)
	}

	result := *req
	result.Messages = coreMessages
	return &result, nil
}

func tailMatchedSystemOffsets(originalSystemCount, modifiedSystemCount int) (matchStart, originalStart int) {
	matched := min(modifiedSystemCount, originalSystemCount)
	return modifiedSystemCount - matched, originalSystemCount - matched
}

func applyGuardedMessageToOriginal(original core.Message, modified Message) (core.Message, error) {
	preserved := cloneChatMessageEnvelope(original)
	preserved.Role = modified.Role
	preserved.ToolCalls = cloneToolCalls(modified.ToolCalls)
	preserved.ToolCallID = modified.ToolCallID

	content, contentNull, err := applyGuardedContentToOriginal(original.Content, modified.Content, modified.ContentNull)
	if err != nil {
		return core.Message{}, err
	}
	preserved.Content = content
	preserved.ContentNull = contentNull
	return preserved, nil
}

func newChatMessageFromGuardrail(m Message) core.Message {
	contentNull := m.ContentNull
	if m.Content != "" {
		contentNull = false
	}

	content := any(m.Content)
	if contentNull {
		content = nil
	}

	return core.Message{
		Role:        m.Role,
		Content:     content,
		ToolCalls:   cloneToolCalls(m.ToolCalls),
		ToolCallID:  m.ToolCallID,
		ContentNull: contentNull,
	}
}

func applyGuardedContentToOriginal(originalContent any, rewrittenText string, contentNull bool) (any, bool, error) {
	if core.HasStructuredContent(originalContent) {
		mergedContent, err := rewriteStructuredContentWithTextRewrite(originalContent, rewrittenText)
		if err != nil {
			return nil, false, err
		}
		return mergedContent, false, nil
	}

	if rewrittenText != "" {
		contentNull = false
	}
	if contentNull {
		return nil, true, nil
	}
	return rewrittenText, false, nil
}

func rewriteStructuredContentWithTextRewrite(originalContent any, rewrittenText string) (any, error) {
	parts, ok := core.NormalizeContentParts(originalContent)
	if !ok {
		return nil, core.NewInvalidRequestError("guardrails cannot merge rewritten text into structured message", nil)
	}

	// Guard against pathological numbers of content parts that could cause size
	// computations for allocations to overflow on some platforms.
	const maxContentParts = 1_000_000
	if len(parts) >= maxContentParts {
		return nil, core.NewInvalidRequestError("guardrails cannot merge structured message with too many content parts", nil)
	}

	originalTexts := make([]string, 0, len(parts))
	textPartIndexes := make([]int, 0, len(parts))
	for i, part := range parts {
		if part.Type == "text" {
			textPartIndexes = append(textPartIndexes, i)
			originalTexts = append(originalTexts, part.Text)
		}
	}

	if len(textPartIndexes) == 0 {
		merged := cloneContentParts(parts)
		if rewrittenText != "" {
			merged = append([]core.ContentPart{{Type: "text", Text: rewrittenText}}, merged...)
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured message after rewrite", nil)
		}
		return merged, nil
	}

	if len(textPartIndexes) == 1 {
		merged := cloneContentParts(parts)
		textIndex := textPartIndexes[0]
		if rewrittenText == "" {
			merged = append(merged[:textIndex], merged[textIndex+1:]...)
		} else {
			merged[textIndex].Text = rewrittenText
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured message after rewrite", nil)
		}
		return merged, nil
	}

	if rewrittenText == strings.Join(originalTexts, " ") {
		return cloneContentParts(parts), nil
	}

	merged := make([]core.ContentPart, 0, len(parts))
	insertedRewrittenText := false
	for _, part := range parts {
		if part.Type == "text" {
			if !insertedRewrittenText && rewrittenText != "" {
				rewrittenPart := cloneContentPart(part)
				rewrittenPart.Text = rewrittenText
				merged = append(merged, rewrittenPart)
				insertedRewrittenText = true
			}
			continue
		}
		merged = append(merged, cloneContentPart(part))
	}

	if len(merged) == 0 {
		return nil, core.NewInvalidRequestError("guardrails produced empty structured message after rewrite", nil)
	}
	return merged, nil
}

func normalizeGuardrailMessageText(content any) (string, error) {
	normalizedContent := content
	switch typed := content.(type) {
	case []map[string]any:
		parts := make([]any, len(typed))
		for i, part := range typed {
			parts[i] = part
		}
		normalizedContent = parts
	default:
		value := reflect.ValueOf(content)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			switch content.(type) {
			case []any, []core.ContentPart:
			default:
				parts := make([]any, value.Len())
				for i := 0; i < value.Len(); i++ {
					parts[i] = value.Index(i).Interface()
				}
				normalizedContent = parts
			}
		}
	}

	normalized, err := core.NormalizeMessageContent(normalizedContent)
	if err != nil {
		return "", err
	}
	return core.ExtractTextContent(normalized), nil
}

// responsesToMessages extracts the normalized message list from a ResponsesRequest.
// The Instructions field maps to a system message and the input items are kept in
// one-to-one order so content-rewriting guardrails can be applied back safely.
func responsesToMessages(req *core.ResponsesRequest) ([]Message, error) {
	var msgs []Message
	if req.Instructions != "" {
		msgs = append(msgs, Message{Role: "system", Content: req.Instructions})
	}

	inputMsgs, err := responsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, inputMsgs...)
	return msgs, nil
}

func responsesInputToMessages(input any) ([]Message, error) {
	switch typed := input.(type) {
	case nil:
		return nil, nil
	case string:
		return []Message{{Role: "user", Content: typed}}, nil
	}

	elements, err := coerceResponsesInputElements(input)
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, len(elements))
	for i, element := range elements {
		msg, err := responsesInputElementToGuardrailMessage(element, i)
		if err != nil {
			return nil, err
		}
		msgs[i] = msg
	}
	return msgs, nil
}

func coerceResponsesInputElements(input any) ([]core.ResponsesInputElement, error) {
	switch typed := input.(type) {
	case []core.ResponsesInputElement:
		elements := make([]core.ResponsesInputElement, len(typed))
		copy(elements, typed)
		return elements, nil
	case []map[string]any:
		elements := make([]core.ResponsesInputElement, len(typed))
		for i, item := range typed {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
			if err := json.Unmarshal(raw, &elements[i]); err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
		}
		return elements, nil
	case []any:
		elements := make([]core.ResponsesInputElement, len(typed))
		for i, item := range typed {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
			if err := json.Unmarshal(raw, &elements[i]); err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
		}
		return elements, nil
	default:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	}
}

func responsesInputElementToGuardrailMessage(item core.ResponsesInputElement, index int) (Message, error) {
	switch item.Type {
	case "function_call":
		if strings.TrimSpace(item.Name) == "" {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: function_call name is required", nil)
		}
		return Message{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:   item.CallID,
					Type: "function",
					Function: core.FunctionCall{
						Name:      item.Name,
						Arguments: item.Arguments,
					},
				},
			},
		}, nil
	case "function_call_output":
		content, err := stringifyResponsesValue(item.Output)
		if err != nil {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: function_call_output.output must be JSON-serializable", err)
		}
		return Message{
			Role:       "tool",
			ToolCallID: item.CallID,
			Content:    content,
		}, nil
	default:
		role := strings.TrimSpace(item.Role)
		if role == "" {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: role is required", nil)
		}
		text, err := normalizeGuardrailMessageText(item.Content)
		if err != nil {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: unsupported content", err)
		}
		return Message{
			Role:        role,
			Content:     text,
			ContentNull: item.Content == nil,
		}, nil
	}
}

func stringifyResponsesValue(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}

// applyMessagesToResponses returns a shallow copy of req with system and input
// messages applied back to the original Responses envelope.
func applyMessagesToResponses(req *core.ResponsesRequest, msgs []Message) (*core.ResponsesRequest, error) {
	result := *req
	originalInputMsgs, err := responsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}

	inputMsgs := msgs
	switch {
	case len(msgs) == len(originalInputMsgs):
		result.Instructions = ""
	case len(msgs) == len(originalInputMsgs)+1 && len(msgs) > 0 && msgs[0].Role == "system":
		result.Instructions = msgs[0].Content
		inputMsgs = msgs[1:]
	default:
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}

	input, err := applyMessagesToResponsesInput(req.Input, inputMsgs)
	if err != nil {
		return nil, err
	}
	result.Input = input
	return &result, nil
}

func applyMessagesToResponsesInput(original any, msgs []Message) (any, error) {
	switch typed := original.(type) {
	case nil:
		if len(msgs) != 0 {
			return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
		}
		return nil, nil
	case string:
		if len(msgs) != 1 {
			return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
		}
		if msgs[0].Role != "user" {
			return nil, core.NewInvalidRequestError("guardrails cannot change the role of a string responses input", nil)
		}
		if msgs[0].ContentNull {
			return "", nil
		}
		return msgs[0].Content, nil
	default:
		_ = typed
	}

	elements, err := coerceResponsesInputElements(original)
	if err != nil {
		return nil, err
	}
	patched, err := applyMessagesToResponsesElements(elements, msgs)
	if err != nil {
		return nil, err
	}
	return patchResponsesInputEnvelope(original, patched)
}

func applyMessagesToResponsesElements(elements []core.ResponsesInputElement, msgs []Message) ([]core.ResponsesInputElement, error) {
	if len(msgs) != len(elements) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}

	result := make([]core.ResponsesInputElement, len(elements))
	for i, original := range elements {
		patched, err := applyGuardedResponsesElementToOriginal(original, msgs[i], i)
		if err != nil {
			return nil, err
		}
		result[i] = patched
	}
	return result, nil
}

func applyGuardedResponsesElementToOriginal(original core.ResponsesInputElement, modified Message, _ int) (core.ResponsesInputElement, error) {
	preserved := original

	switch original.Type {
	case "function_call":
		if modified.Role != "assistant" {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot reorder or retag responses input items", nil)
		}
		return preserved, nil
	case "function_call_output":
		if modified.Role != "tool" {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot reorder or retag responses input items", nil)
		}
		preserved.Output = modified.Content
		return preserved, nil
	default:
		role := strings.TrimSpace(original.Role)
		if role == "" {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("invalid responses input item: role is required", nil)
		}
		if modified.Role != role {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot reorder or retag responses input items", nil)
		}
		content, err := applyGuardedResponsesContentToOriginal(original.Content, modified.Content, modified.ContentNull)
		if err != nil {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot merge rewritten text into responses input item", err)
		}
		preserved.Content = content
		return preserved, nil
	}
}

func applyGuardedResponsesContentToOriginal(originalContent any, rewrittenText string, contentNull bool) (any, error) {
	if isResponsesStructuredContent(originalContent) {
		return rewriteStructuredResponsesContentWithTextRewrite(originalContent, rewrittenText)
	}
	if rewrittenText != "" {
		contentNull = false
	}
	if contentNull {
		return nil, nil
	}
	return rewrittenText, nil
}

func patchResponsesInputEnvelope(original any, patched []core.ResponsesInputElement) (any, error) {
	switch typed := original.(type) {
	case []core.ResponsesInputElement:
		result := make([]core.ResponsesInputElement, len(patched))
		copy(result, patched)
		return result, nil
	case []map[string]any:
		return patchResponsesInputMapSlice(typed, patched)
	case []any:
		return patchResponsesInputInterfaceSlice(typed, patched)
	default:
		return patched, nil
	}
}

func patchResponsesInputMapSlice(original []map[string]any, patched []core.ResponsesInputElement) ([]map[string]any, error) {
	if len(original) != len(patched) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}
	result := make([]map[string]any, len(original))
	for i := range original {
		item, err := patchResponsesInputMap(original[i], patched[i])
		if err != nil {
			return nil, err
		}
		result[i] = item
	}
	return result, nil
}

func patchResponsesInputInterfaceSlice(original []any, patched []core.ResponsesInputElement) ([]any, error) {
	if len(original) != len(patched) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}
	result := make([]any, len(original))
	for i := range original {
		item, err := patchResponsesInputInterfaceElement(original[i], patched[i])
		if err != nil {
			return nil, err
		}
		result[i] = item
	}
	return result, nil
}

func patchResponsesInputInterfaceElement(original any, patched core.ResponsesInputElement) (any, error) {
	if originalMap, ok := original.(map[string]any); ok {
		return patchResponsesInputMap(originalMap, patched)
	}
	return responsesInputElementAsAny(patched)
}

func patchResponsesInputMap(original map[string]any, patched core.ResponsesInputElement) (map[string]any, error) {
	cloned := cloneStringAnyMap(original)
	updated, err := responsesInputElementAsMap(patched)
	if err != nil {
		return nil, err
	}

	for _, key := range []string{"type", "role", "status", "content", "call_id", "id", "name", "arguments", "output"} {
		delete(cloned, key)
	}
	for key, value := range updated {
		cloned[key] = value
	}
	if patched.Type == "function_call_output" {
		cloned["output"] = restoreResponsesInputOutputValue(original["output"], patched.Output)
	}
	return cloned, nil
}

func restoreResponsesInputOutputValue(original any, rewritten string) any {
	if _, ok := original.(string); ok {
		return rewritten
	}
	if strings.TrimSpace(rewritten) == "" {
		if original == nil {
			return nil
		}
		return original
	}

	var decoded any
	if err := json.Unmarshal([]byte(rewritten), &decoded); err == nil {
		return decoded
	}
	if original == nil {
		return nil
	}
	return original
}

func responsesInputElementAsMap(element core.ResponsesInputElement) (map[string]any, error) {
	value, err := responsesInputElementAsAny(element)
	if err != nil {
		return nil, err
	}
	itemMap, ok := value.(map[string]any)
	if !ok {
		return nil, core.NewInvalidRequestError("invalid responses input item", nil)
	}
	return itemMap, nil
}

func responsesInputElementAsAny(element core.ResponsesInputElement) (any, error) {
	raw, err := json.Marshal(element)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid responses input item", err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, core.NewInvalidRequestError("invalid responses input item", err)
	}
	return value, nil
}

func isResponsesStructuredContent(content any) bool {
	if content == nil {
		return false
	}
	switch typed := content.(type) {
	case []any:
		return true
	case []core.ContentPart:
		return true
	case []map[string]any:
		return true
	default:
		_ = typed
	}
	contentType := reflect.TypeOf(content)
	return contentType.Kind() == reflect.Slice || contentType.Kind() == reflect.Array
}

func rewriteStructuredResponsesContentWithTextRewrite(originalContent any, rewrittenText string) (any, error) {
	switch typed := originalContent.(type) {
	case []core.ContentPart:
		return rewriteStructuredResponsesTypedContentParts(typed, rewrittenText)
	case []any:
		return rewriteStructuredResponsesInterfaceContentParts(typed, rewrittenText)
	case []map[string]any:
		return rewriteStructuredResponsesMapContentParts(typed, rewrittenText)
	default:
		value := reflect.ValueOf(originalContent)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
			return nil, core.NewInvalidRequestError("unsupported structured responses content", nil)
		}
		parts := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			parts[i] = value.Index(i).Interface()
		}
		return rewriteStructuredResponsesInterfaceContentParts(parts, rewrittenText)
	}
}

func rewriteStructuredResponsesTypedContentParts(parts []core.ContentPart, rewrittenText string) (any, error) {
	textIndexes := make([]int, 0, len(parts))
	originalTexts := make([]string, 0, len(parts))
	for i, part := range parts {
		if !isResponsesTextPartType(part.Type) || part.Text == "" {
			continue
		}
		textIndexes = append(textIndexes, i)
		originalTexts = append(originalTexts, part.Text)
	}

	if len(textIndexes) == 0 {
		if rewrittenText == "" {
			if len(parts) == 0 {
				return []core.ContentPart{}, nil
			}
			return cloneContentParts(parts), nil
		}
		prepended := []core.ContentPart{{Type: "input_text", Text: rewrittenText}}
		prepended = append(prepended, cloneContentParts(parts)...)
		return prepended, nil
	}

	if len(textIndexes) == 1 {
		merged := cloneContentParts(parts)
		textIndex := textIndexes[0]
		if rewrittenText == "" {
			merged = append(merged[:textIndex], merged[textIndex+1:]...)
		} else {
			merged[textIndex].Text = rewrittenText
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
		}
		return merged, nil
	}

	if rewrittenText == strings.Join(originalTexts, " ") {
		return cloneContentParts(parts), nil
	}

	merged := make([]core.ContentPart, 0, len(parts))
	insertedRewrittenText := false
	for _, part := range parts {
		if isResponsesTextPartType(part.Type) {
			if !insertedRewrittenText && rewrittenText != "" {
				rewrittenPart := cloneContentPart(part)
				rewrittenPart.Text = rewrittenText
				merged = append(merged, rewrittenPart)
				insertedRewrittenText = true
			}
			continue
		}
		merged = append(merged, cloneContentPart(part))
	}

	if len(merged) == 0 {
		return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
	}
	return merged, nil
}

func rewriteStructuredResponsesInterfaceContentParts(parts []any, rewrittenText string) (any, error) {
	textIndexes := make([]int, 0, len(parts))
	originalTexts := make([]string, 0, len(parts))
	for i, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := partMap["type"].(string)
		if !isResponsesTextPartType(partType) {
			continue
		}
		text, _ := partMap["text"].(string)
		if text == "" {
			continue
		}
		textIndexes = append(textIndexes, i)
		originalTexts = append(originalTexts, text)
	}

	if len(textIndexes) == 0 {
		if rewrittenText == "" {
			if len(parts) == 0 {
				return []any{}, nil
			}
			return cloneResponsesInterfaceParts(parts), nil
		}
		prepended := []any{map[string]any{"type": "input_text", "text": rewrittenText}}
		prepended = append(prepended, cloneResponsesInterfaceParts(parts)...)
		return prepended, nil
	}

	if len(textIndexes) == 1 {
		merged := make([]any, 0, len(parts))
		textIndex := textIndexes[0]
		for i, part := range parts {
			if i == textIndex {
				if rewrittenText == "" {
					continue
				}
				partMap, ok := part.(map[string]any)
				if !ok {
					return nil, core.NewInvalidRequestError("guardrails cannot rewrite non-object responses content part", nil)
				}
				cloned := cloneStringAnyMap(partMap)
				cloned["text"] = rewrittenText
				merged = append(merged, cloned)
				continue
			}
			merged = append(merged, cloneResponsesInterfacePart(part))
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
		}
		return merged, nil
	}

	if rewrittenText == strings.Join(originalTexts, " ") {
		return cloneResponsesInterfaceParts(parts), nil
	}

	merged := make([]any, 0, len(parts))
	insertedRewrittenText := false
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if ok {
			partType, _ := partMap["type"].(string)
			if isResponsesTextPartType(partType) {
				if !insertedRewrittenText && rewrittenText != "" {
					cloned := cloneStringAnyMap(partMap)
					cloned["text"] = rewrittenText
					merged = append(merged, cloned)
					insertedRewrittenText = true
				}
				continue
			}
		}
		merged = append(merged, cloneResponsesInterfacePart(part))
	}

	if len(merged) == 0 {
		return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
	}
	return merged, nil
}

func rewriteStructuredResponsesMapContentParts(parts []map[string]any, rewrittenText string) (any, error) {
	if len(parts) == 0 && rewrittenText == "" {
		return []map[string]any{}, nil
	}

	interfaceParts := make([]any, len(parts))
	for i, part := range parts {
		interfaceParts[i] = part
	}
	rewritten, err := rewriteStructuredResponsesInterfaceContentParts(interfaceParts, rewrittenText)
	if err != nil {
		return nil, err
	}
	rewrittenParts, ok := rewritten.([]any)
	if !ok {
		return nil, core.NewInvalidRequestError("unsupported structured responses content", nil)
	}

	result := make([]map[string]any, len(rewrittenParts))
	for i, part := range rewrittenParts {
		partMap, ok := part.(map[string]any)
		if !ok {
			return nil, core.NewInvalidRequestError("guardrails cannot rewrite non-object responses content part", nil)
		}
		result[i] = cloneStringAnyMap(partMap)
	}
	return result, nil
}

func isResponsesTextPartType(partType string) bool {
	switch partType {
	case "text", "input_text", "output_text":
		return true
	default:
		return false
	}
}

func cloneResponsesInterfaceParts(parts []any) []any {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]any, len(parts))
	for i, part := range parts {
		cloned[i] = cloneResponsesInterfacePart(part)
	}
	return cloned
}

func cloneResponsesInterfacePart(part any) any {
	partMap, ok := part.(map[string]any)
	if !ok {
		return part
	}
	return cloneStringAnyMap(partMap)
}

// cloneStringAnyMap performs a shallow copy of the map. Nested maps/slices are
// intentionally shared; callers are expected to either preserve them as-is or
// replace whole top-level values instead of mutating nested structures in place.
func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	cloned := make(map[string]any, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
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
