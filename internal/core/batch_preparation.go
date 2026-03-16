package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// BatchPreparationMetadata captures request-scoped batch preprocessing effects
// that are useful for persistence and debugging but should not be exposed as
// public API fields automatically.
type BatchPreparationMetadata struct {
	OriginalInputFileID  string
	RewrittenInputFileID string
}

// RecordInputFileRewrite tracks the first user-supplied provider file id and
// the latest derived provider file id submitted upstream.
func (m *BatchPreparationMetadata) RecordInputFileRewrite(original, rewritten string) {
	if m == nil {
		return
	}
	original = strings.TrimSpace(original)
	rewritten = strings.TrimSpace(rewritten)
	if original == "" || rewritten == "" || original == rewritten {
		return
	}
	if m.OriginalInputFileID == "" {
		m.OriginalInputFileID = original
	}
	m.RewrittenInputFileID = rewritten
}

// BatchFileTransport is the minimal provider-native file API surface needed to
// preprocess file-backed batch requests.
type BatchFileTransport interface {
	GetFileContent(ctx context.Context, providerType, id string) (*FileContentResponse, error)
	CreateFile(ctx context.Context, providerType string, req *FileCreateRequest) (*FileObject, error)
}

// BatchItemRewriteFunc rewrites a decoded batch item body for provider submission.
// The original batch item is provided so callers can preserve non-semantic JSON
// structure when needed.
type BatchItemRewriteFunc func(context.Context, BatchRequestItem, *DecodedBatchItemRequest) (json.RawMessage, error)

// BatchRewriteResult captures the normalized request plus any gateway-only
// metadata derived while rewriting inline or file-backed batch sources.
type BatchRewriteResult struct {
	Request              *BatchRequest
	RequestEndpointHints map[string]string
	OriginalInputFileID  string
	RewrittenInputFileID string
}

// RewriteBatchSource normalizes both inline and file-backed batch sources
// using the same typed per-item rewrite callback.
func RewriteBatchSource(
	ctx context.Context,
	providerType string,
	req *BatchRequest,
	fileTransport BatchFileTransport,
	operations []Operation,
	rewrite BatchItemRewriteFunc,
) (*BatchRewriteResult, error) {
	if req == nil {
		return nil, NewInvalidRequestError("batch request is required", nil)
	}
	if rewrite == nil {
		return nil, NewInvalidRequestError("batch rewrite function is required", nil)
	}

	forward := cloneBatchRequest(req)
	result := &BatchRewriteResult{Request: forward}
	hints := map[string]string{}

	if len(forward.Requests) > 0 {
		inlineHints, err := rewriteInlineBatchItems(ctx, forward, operations, rewrite)
		if err != nil {
			return nil, err
		}
		mergeBatchEndpointHints(hints, inlineHints)
	}

	if strings.TrimSpace(forward.InputFileID) != "" {
		if fileTransport == nil {
			return nil, NewInvalidRequestError("file routing is not supported by the current provider router", nil)
		}
		fileResult, err := rewriteInputFileBatch(ctx, providerType, forward, fileTransport, operations, rewrite)
		if err != nil {
			return nil, err
		}
		result.OriginalInputFileID = fileResult.OriginalInputFileID
		result.RewrittenInputFileID = fileResult.RewrittenInputFileID
		mergeBatchEndpointHints(hints, fileResult.RequestEndpointHints)
	}

	if len(hints) > 0 {
		result.RequestEndpointHints = hints
	}
	return result, nil
}

func rewriteInlineBatchItems(ctx context.Context, req *BatchRequest, operations []Operation, rewrite BatchItemRewriteFunc) (map[string]string, error) {
	hints := map[string]string{}
	for i := range req.Requests {
		item := req.Requests[i]
		recordBatchEndpointHint(hints, req.Endpoint, item)

		decoded, handled, err := MaybeDecodeKnownBatchItemRequest(req.Endpoint, item, operations...)
		if err != nil {
			return nil, NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}
		if !handled {
			continue
		}

		body, err := rewrite(ctx, item, decoded)
		if err != nil {
			return nil, err
		}
		req.Requests[i].Body = CloneRawJSON(body)
	}
	if len(hints) == 0 {
		return nil, nil
	}
	return hints, nil
}

func rewriteInputFileBatch(
	ctx context.Context,
	providerType string,
	req *BatchRequest,
	fileTransport BatchFileTransport,
	operations []Operation,
	rewrite BatchItemRewriteFunc,
) (*BatchRewriteResult, error) {
	resp, err := fileTransport.GetFileContent(ctx, providerType, req.InputFileID)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, NewProviderError(providerType, http.StatusBadGateway, "provider returned empty file content response", nil)
	}

	rewrittenContent, hints, changed, err := rewriteBatchJSONLContent(ctx, req.Endpoint, resp.Data, operations, rewrite)
	if err != nil {
		return nil, err
	}

	result := &BatchRewriteResult{
		OriginalInputFileID: req.InputFileID,
	}
	if len(hints) > 0 {
		result.RequestEndpointHints = hints
	}
	if !changed {
		return result, nil
	}

	filename := strings.TrimSpace(resp.Filename)
	if filename == "" {
		filename = "batch-input.jsonl"
	}

	created, err := fileTransport.CreateFile(ctx, providerType, &FileCreateRequest{
		Purpose:  "batch",
		Filename: filename,
		Content:  rewrittenContent,
	})
	if err != nil {
		return nil, err
	}
	if created == nil || strings.TrimSpace(created.ID) == "" {
		return nil, NewProviderError(providerType, http.StatusBadGateway, "provider returned empty file object for rewritten batch input", nil)
	}

	req.InputFileID = created.ID
	result.RewrittenInputFileID = created.ID
	return result, nil
}

func rewriteBatchJSONLContent(
	ctx context.Context,
	defaultEndpoint string,
	data []byte,
	operations []Operation,
	rewrite BatchItemRewriteFunc,
) ([]byte, map[string]string, bool, error) {
	if len(data) == 0 {
		return data, nil, false, nil
	}

	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	hints := map[string]string{}
	var out bytes.Buffer
	changed := false

	for i, rawLine := range lines {
		lineNo := i + 1
		hasNewline := bytes.HasSuffix(rawLine, []byte("\n"))
		line := rawLine
		if hasNewline {
			line = line[:len(line)-1]
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(bytes.TrimSpace(line)) == 0 {
			out.Write(rawLine)
			continue
		}

		var item BatchRequestItem
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, nil, false, NewInvalidRequestError(
				fmt.Sprintf("invalid batch input file line %d: %s", lineNo, err.Error()),
				err,
			)
		}
		recordBatchEndpointHint(hints, defaultEndpoint, item)

		decoded, handled, err := MaybeDecodeKnownBatchItemRequest(defaultEndpoint, item, operations...)
		if err != nil {
			return nil, nil, false, wrapBatchInputFileLineError(lineNo, err)
		}
		if !handled {
			out.Write(rawLine)
			continue
		}

		body, err := rewrite(ctx, item, decoded)
		if err != nil {
			return nil, nil, false, wrapBatchInputFileLineError(lineNo, err)
		}
		if jsonSemanticallyEqual(item.Body, body) {
			out.Write(rawLine)
			continue
		}

		item.Body = CloneRawJSON(body)
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, nil, false, NewInvalidRequestError(
				fmt.Sprintf("failed to encode batch input file line %d", lineNo),
				err,
			)
		}
		out.Write(encoded)
		if hasNewline {
			out.WriteByte('\n')
		}
		changed = true
	}

	if len(hints) == 0 {
		hints = nil
	}
	return out.Bytes(), hints, changed, nil
}

func wrapBatchInputFileLineError(lineNo int, err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		if gatewayErr.Type == ErrorTypeInvalidRequest {
			return NewInvalidRequestError(fmt.Sprintf("batch input file line %d: %s", lineNo, gatewayErr.Message), err)
		}
		return err
	}
	return NewInvalidRequestError(fmt.Sprintf("batch input file line %d: %s", lineNo, err.Error()), err)
}

func recordBatchEndpointHint(hints map[string]string, defaultEndpoint string, item BatchRequestItem) {
	if hints == nil || strings.TrimSpace(item.CustomID) == "" {
		return
	}
	endpoint := NormalizeOperationPath(ResolveBatchItemEndpoint(defaultEndpoint, item.URL))
	if endpoint == "" {
		return
	}
	hints[item.CustomID] = endpoint
}

func mergeBatchEndpointHints(dst, src map[string]string) {
	if len(src) == 0 {
		return
	}
	for customID, endpoint := range src {
		dst[customID] = endpoint
	}
}

func cloneBatchRequest(req *BatchRequest) *BatchRequest {
	if req == nil {
		return nil
	}

	cloned := &BatchRequest{
		InputFileID:      req.InputFileID,
		Endpoint:         req.Endpoint,
		CompletionWindow: req.CompletionWindow,
		Metadata:         cloneBatchStringMap(req.Metadata),
		ExtraFields:      CloneRawJSONMap(req.ExtraFields),
	}
	if req.Requests != nil {
		cloned.Requests = make([]BatchRequestItem, len(req.Requests))
		for i, item := range req.Requests {
			cloned.Requests[i] = cloneBatchRequestItem(item)
		}
	}
	return cloned
}

func cloneBatchRequestItem(item BatchRequestItem) BatchRequestItem {
	return BatchRequestItem{
		CustomID:    item.CustomID,
		Method:      item.Method,
		URL:         item.URL,
		Body:        CloneRawJSON(item.Body),
		ExtraFields: CloneRawJSONMap(item.ExtraFields),
	}
}

func cloneBatchStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	cloned := make(map[string]string, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	var rightValue any
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	return reflectDeepEqualJSON(leftValue, rightValue)
}

func reflectDeepEqualJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}
