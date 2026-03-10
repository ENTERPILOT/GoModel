package guardrails

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	neturl "net/url"
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

// ChatCompletion extracts messages, applies guardrails, then routes the request.
func (g *GuardedProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	modified, err := g.processChat(ctx, req)
	if err != nil {
		return nil, err
	}
	return g.inner.ChatCompletion(ctx, modified)
}

// StreamChatCompletion extracts messages, applies guardrails, then routes the streaming request.
func (g *GuardedProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	modified, err := g.processChat(ctx, req)
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
	modified, err := g.processResponses(ctx, req)
	if err != nil {
		return nil, err
	}
	return g.inner.Responses(ctx, modified)
}

// StreamResponses extracts messages, applies guardrails, then routes the streaming request.
func (g *GuardedProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	modified, err := g.processResponses(ctx, req)
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

func (g *GuardedProvider) nativeFileRouter() (core.NativeFileRoutableProvider, error) {
	fp, ok := g.inner.(core.NativeFileRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("file routing is not supported by the current provider router", nil)
	}
	return fp, nil
}

func (g *GuardedProvider) passthroughRouter() (core.PassthroughRoutableProvider, error) {
	pp, ok := g.inner.(core.PassthroughRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("passthrough routing is not supported by the current provider router", nil)
	}
	return pp, nil
}

func (g *GuardedProvider) normalizeBatchEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := neturl.Parse(trimmed); err == nil && parsed.Path != "" {
		trimmed = parsed.Path
	}
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func (g *GuardedProvider) processBatchRequest(ctx context.Context, req *core.BatchRequest) (*core.BatchRequest, error) {
	if req == nil || len(req.Requests) == 0 {
		return req, nil
	}

	out := *req
	out.Requests = make([]core.BatchRequestItem, len(req.Requests))
	copy(out.Requests, req.Requests)

	for i := range out.Requests {
		item := out.Requests[i]
		method := strings.ToUpper(strings.TrimSpace(item.Method))
		if method == "" {
			method = http.MethodPost
		}
		if method != http.MethodPost || len(item.Body) == 0 {
			continue
		}

		endpoint := strings.TrimSpace(item.URL)
		if endpoint == "" {
			endpoint = strings.TrimSpace(req.Endpoint)
		}

		switch g.normalizeBatchEndpoint(endpoint) {
		case "/v1/chat/completions":
			var chatReq core.ChatRequest
			if err := json.Unmarshal(item.Body, &chatReq); err != nil {
				return nil, core.NewInvalidRequestError("invalid chat request in batch item", err)
			}
			modified, err := g.processChat(ctx, &chatReq)
			if err != nil {
				return nil, err
			}
			body, err := rewriteGuardedChatBatchBody(item.Body, &chatReq, modified)
			if err != nil {
				return nil, core.NewInvalidRequestError("failed to encode guarded chat batch item", err)
			}
			out.Requests[i].Body = body
		case "/v1/responses":
			var responsesReq core.ResponsesRequest
			if err := json.Unmarshal(item.Body, &responsesReq); err != nil {
				return nil, core.NewInvalidRequestError("invalid responses request in batch item", err)
			}
			modified, err := g.processResponses(ctx, &responsesReq)
			if err != nil {
				return nil, err
			}
			body, err := rewriteGuardedResponsesBatchBody(item.Body, modified)
			if err != nil {
				return nil, core.NewInvalidRequestError("failed to encode guarded responses batch item", err)
			}
			out.Requests[i].Body = body
		}
	}

	return &out, nil
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

	switch {
	case len(originalRawItems) == len(modified) && len(original) == len(modified):
		return marshalPatchedChatMessages(originalRawItems, modified)
	case len(originalRawItems) == len(original) && len(modified) == len(original)+1 && len(modified) > 0 && modified[0].Role == "system":
		patchedOriginals, err := marshalPatchedChatMessages(originalRawItems, modified[1:])
		if err != nil {
			return nil, err
		}
		systemMessage, err := json.Marshal(modified[0])
		if err != nil {
			return nil, err
		}
		var patchedOriginalItems []json.RawMessage
		if err := json.Unmarshal(patchedOriginals, &patchedOriginalItems); err != nil {
			return nil, err
		}
		return json.Marshal(append([]json.RawMessage{systemMessage}, patchedOriginalItems...))
	case len(originalRawItems) == len(original) && len(modified) > 0 && modified[0].Role == "system":
		originalNonSystem := make([]json.RawMessage, 0, len(originalRawItems))
		modifiedNonSystem := make([]core.Message, 0, len(modified))
		for i, msg := range original {
			if msg.Role != "system" {
				originalNonSystem = append(originalNonSystem, originalRawItems[i])
			}
		}
		modifiedNonSystem = append(modifiedNonSystem, modified[1:]...)
		if len(originalNonSystem) != len(modifiedNonSystem) {
			break
		}
		patchedOriginals, err := marshalPatchedChatMessages(originalNonSystem, modifiedNonSystem)
		if err != nil {
			return nil, err
		}
		systemMessage, err := json.Marshal(modified[0])
		if err != nil {
			return nil, err
		}
		var patchedOriginalItems []json.RawMessage
		if err := json.Unmarshal(patchedOriginals, &patchedOriginalItems); err != nil {
			return nil, err
		}
		return json.Marshal(append([]json.RawMessage{systemMessage}, patchedOriginalItems...))
	}

	return json.Marshal(modified)
}

func marshalPatchedChatMessages(originals []json.RawMessage, modified []core.Message) (json.RawMessage, error) {
	if len(originals) != len(modified) {
		return nil, core.NewInvalidRequestError("guardrails changed chat message cardinality unexpectedly", nil)
	}

	patched := make([]json.RawMessage, len(modified))
	for i := range modified {
		item, err := patchRawChatMessage(originals[i], modified[i])
		if err != nil {
			return nil, err
		}
		patched[i] = item
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

	modifiedReq, err := g.processBatchRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return bp.CreateBatch(ctx, providerType, modifiedReq)
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

// processChat runs the pipeline for a ChatRequest via the message adapter.
func (g *GuardedProvider) processChat(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	msgs, err := chatToMessages(req)
	if err != nil {
		return nil, err
	}
	modified, err := g.pipeline.Process(ctx, msgs)
	if err != nil {
		return nil, err
	}
	if chatNeedsEnvelopePreservation(req) {
		return applySystemMessagesToMultimodalChat(req, modified)
	}
	return applyMessagesToChat(req, modified), nil
}

// processResponses runs the pipeline for a ResponsesRequest via the message adapter.
func (g *GuardedProvider) processResponses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	msgs := responsesToMessages(req)
	modified, err := g.pipeline.Process(ctx, msgs)
	if err != nil {
		return nil, err
	}
	return applyMessagesToResponses(req, modified), nil
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

// applyMessagesToChat returns a shallow copy of req with messages replaced.
func applyMessagesToChat(req *core.ChatRequest, msgs []Message) *core.ChatRequest {
	coreMessages := make([]core.Message, len(msgs))
	for i, m := range msgs {
		contentNull := m.ContentNull
		if m.Content != "" {
			contentNull = false
		}
		coreMessages[i] = core.Message{
			Role:        m.Role,
			Content:     m.Content,
			ToolCalls:   cloneToolCalls(m.ToolCalls),
			ToolCallID:  m.ToolCallID,
			ContentNull: contentNull,
		}
	}
	result := *req
	result.Messages = coreMessages
	return &result
}

// applySystemMessagesToMultimodalChat applies system-message updates and preserves
// original content only for messages that contain non-text multimodal parts.
// Text-only messages keep guardrail-rewritten text.
func applySystemMessagesToMultimodalChat(req *core.ChatRequest, msgs []Message) (*core.ChatRequest, error) {
	nonSystemOriginal := make([]core.Message, 0, len(req.Messages))
	for _, original := range req.Messages {
		if original.Role != "system" {
			nonSystemOriginal = append(nonSystemOriginal, original)
		}
	}

	coreMessages := make([]core.Message, 0, len(msgs))
	nextNonSystem := 0
	modifiedNonSystemCount := 0
	for _, modified := range msgs {
		if modified.Role == "system" {
			coreMessages = append(coreMessages, core.Message{Role: "system", Content: modified.Content})
			continue
		}
		modifiedNonSystemCount++
		if nextNonSystem >= len(nonSystemOriginal) {
			return nil, core.NewInvalidRequestError("guardrails cannot insert non-system multimodal or tool-call messages", nil)
		}
		original := nonSystemOriginal[nextNonSystem]
		if modified.Role != original.Role {
			return nil, core.NewInvalidRequestError("guardrails cannot reorder non-system multimodal or tool-call messages", nil)
		}
		preserved := original
		preserved.Role = modified.Role
		if core.HasNonTextContent(original.Content) {
			mergedContent, err := mergeMultimodalContentWithTextRewrite(original.Content, modified.Content)
			if err != nil {
				return nil, err
			}
			preserved.Content = mergedContent
		} else {
			preserved.Content = modified.Content
		}
		coreMessages = append(coreMessages, preserved)
		nextNonSystem++
	}

	if modifiedNonSystemCount != len(nonSystemOriginal) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove non-system multimodal or tool-call messages", nil)
	}

	result := *req
	result.Messages = coreMessages
	return &result, nil
}

func mergeMultimodalContentWithTextRewrite(originalContent any, rewrittenText string) (any, error) {
	parts, ok := core.NormalizeContentParts(originalContent)
	if !ok {
		return nil, core.NewInvalidRequestError("guardrails cannot merge rewritten text into multimodal message", nil)
	}

	// Guard against pathological numbers of content parts that could cause size
	// computations for allocations to overflow on some platforms.
	const maxContentParts = 1_000_000
	if len(parts) >= maxContentParts {
		return nil, core.NewInvalidRequestError("guardrails cannot merge multimodal message with too many content parts", nil)
	}

	capacity := len(parts) + 1
	merged := make([]core.ContentPart, 0, capacity)
	hadTextPart := false
	insertedRewrittenText := false
	textPartCount := 0
	originalTexts := make([]string, 0, len(parts))

	for _, part := range parts {
		if part.Type == "text" {
			textPartCount++
			hadTextPart = true
			originalTexts = append(originalTexts, part.Text)
			if !insertedRewrittenText {
				if rewrittenText != "" {
					merged = append(merged, core.ContentPart{Type: "text", Text: rewrittenText})
				}
				insertedRewrittenText = true
			}
			continue
		}
		merged = append(merged, part)
	}

	if textPartCount > 1 && rewrittenText == strings.Join(originalTexts, " ") {
		copied := make([]core.ContentPart, len(parts))
		copy(copied, parts)
		return copied, nil
	}

	if !hadTextPart && rewrittenText != "" {
		merged = append([]core.ContentPart{{Type: "text", Text: rewrittenText}}, merged...)
	}

	if len(merged) == 0 {
		return nil, core.NewInvalidRequestError("guardrails produced empty multimodal message after rewrite", nil)
	}

	return merged, nil
}

func chatHasNonTextContent(req *core.ChatRequest) bool {
	for _, msg := range req.Messages {
		if core.HasNonTextContent(msg.Content) {
			return true
		}
	}
	return false
}

func chatHasToolCalls(req *core.ChatRequest) bool {
	for _, msg := range req.Messages {
		if len(msg.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func chatNeedsEnvelopePreservation(req *core.ChatRequest) bool {
	return chatHasNonTextContent(req) || chatHasToolCalls(req)
}

func normalizeGuardrailMessageText(content any) (string, error) {
	normalized, err := core.NormalizeMessageContent(content)
	if err != nil {
		return "", err
	}
	return core.ExtractTextContent(normalized), nil
}

// responsesToMessages extracts the normalized message list from a ResponsesRequest.
// The Instructions field maps to a system message.
func responsesToMessages(req *core.ResponsesRequest) []Message {
	var msgs []Message
	if req.Instructions != "" {
		msgs = append(msgs, Message{Role: "system", Content: req.Instructions})
	}
	return msgs
}

// applyMessagesToResponses returns a shallow copy of req with system messages
// applied back to the Instructions field.
func applyMessagesToResponses(req *core.ResponsesRequest, msgs []Message) *core.ResponsesRequest {
	result := *req
	var instructions string
	for _, m := range msgs {
		if m.Role == "system" {
			if instructions != "" {
				instructions += "\n"
			}
			instructions += m.Content
		}
	}
	result.Instructions = instructions
	return &result
}

func cloneToolCalls(toolCalls []core.ToolCall) []core.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	cloned := make([]core.ToolCall, len(toolCalls))
	copy(cloned, toolCalls)
	return cloned
}
