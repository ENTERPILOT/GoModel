package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/aliases"
	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/observability"
	provideradapter "gomodel/internal/providers"
	"gomodel/internal/responsestore"
	"gomodel/internal/usage"
)

func withRequestSnapshotAndPrompt(req *http.Request, frame *core.RequestSnapshot) *http.Request {
	if req == nil || frame == nil {
		return req
	}
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	if prompt := core.DeriveWhiteBoxPrompt(frame); prompt != nil {
		ctx = core.WithWhiteBoxPrompt(ctx, prompt)
	}
	return req.WithContext(ctx)
}

type aliasesTestStore struct {
	aliases []aliases.Alias
}

func newAliasesTestStore(aliasesList ...aliases.Alias) *aliasesTestStore {
	return &aliasesTestStore{aliases: append([]aliases.Alias(nil), aliasesList...)}
}

func (s *aliasesTestStore) List(_ context.Context) ([]aliases.Alias, error) {
	return append([]aliases.Alias(nil), s.aliases...), nil
}

func (s *aliasesTestStore) Get(_ context.Context, name string) (*aliases.Alias, error) {
	for _, alias := range s.aliases {
		if alias.Name == name {
			copy := alias
			return &copy, nil
		}
	}
	return nil, aliases.ErrNotFound
}

func (s *aliasesTestStore) Upsert(_ context.Context, alias aliases.Alias) error {
	for i := range s.aliases {
		if s.aliases[i].Name == alias.Name {
			s.aliases[i] = alias
			return nil
		}
	}
	s.aliases = append(s.aliases, alias)
	return nil
}

func (s *aliasesTestStore) Delete(_ context.Context, name string) error {
	for i := range s.aliases {
		if s.aliases[i].Name == name {
			s.aliases = append(s.aliases[:i], s.aliases[i+1:]...)
			return nil
		}
	}
	return aliases.ErrNotFound
}

func (s *aliasesTestStore) Close() error {
	return nil
}

type failingResponseStore struct {
	err error
}

func (s *failingResponseStore) storeErr() error {
	if s.err != nil {
		return s.err
	}
	return errors.New("response store failed")
}

func (s *failingResponseStore) Create(context.Context, *responsestore.StoredResponse) error {
	return s.storeErr()
}

func (s *failingResponseStore) Get(context.Context, string) (*responsestore.StoredResponse, error) {
	return nil, responsestore.ErrNotFound
}

func (s *failingResponseStore) Update(context.Context, *responsestore.StoredResponse) error {
	return s.storeErr()
}

func (s *failingResponseStore) Delete(context.Context, string) error {
	return responsestore.ErrNotFound
}

func (s *failingResponseStore) Close() error {
	return nil
}

type aliasesTestCatalog struct {
	supported     map[string]bool
	providerTypes map[string]string
	models        map[string]core.Model
}

func (c *aliasesTestCatalog) Supports(model string) bool {
	return c.supported[model]
}

func (c *aliasesTestCatalog) GetProviderType(model string) string {
	return c.providerTypes[model]
}

func (c *aliasesTestCatalog) LookupModel(model string) (*core.Model, bool) {
	entry, ok := c.models[model]
	if !ok {
		return nil, false
	}
	copy := entry
	return &copy, true
}

type chunkedReadCloser struct {
	chunks [][]byte
	index  int
}

func (r *chunkedReadCloser) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}

	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

func (r *chunkedReadCloser) Close() error {
	return nil
}

type flushCountingRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (r *flushCountingRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

type delayedChunkReadCloser struct {
	chunks []delayedChunk
	index  int
}

type delayedChunk struct {
	data    []byte
	delay   time.Duration
	started chan<- struct{}
	release <-chan struct{}
}

func (r *delayedChunkReadCloser) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}

	chunk := r.chunks[r.index]
	r.index++
	if chunk.started != nil {
		close(chunk.started)
		r.chunks[r.index-1].started = nil
	}
	if chunk.delay > 0 {
		time.Sleep(chunk.delay)
	}
	if chunk.release != nil {
		<-chunk.release
	}

	return copy(p, chunk.data), nil
}

func (r *delayedChunkReadCloser) Close() error {
	return nil
}

type streamingProviderWithCustomReader struct {
	mockProvider
	reader io.ReadCloser
}

func (p *streamingProviderWithCustomReader) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.reader, nil
}

type erroringReadCloser struct {
	data []byte
	err  error
	read bool
}

func (r *erroringReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, r.err
	}
	r.read = true
	n := copy(p, r.data)
	if r.err != nil {
		return n, r.err
	}
	return n, io.EOF
}

func (r *erroringReadCloser) Close() error {
	return nil
}

type closeCountingReadCloser struct {
	io.ReadCloser
	closes int
}

func (r *closeCountingReadCloser) Close() error {
	r.closes++
	if r.ReadCloser == nil {
		return nil
	}
	return r.ReadCloser.Close()
}

func setPathParam(c *echo.Context, name, value string) {
	c.SetPathValues(echo.PathValues{{Name: name, Value: value}})
}

type capturingAuditLogger struct {
	config  auditlog.Config
	entries []*auditlog.LogEntry
}

func (l *capturingAuditLogger) Write(entry *auditlog.LogEntry) {
	l.entries = append(l.entries, entry)
}

func (l *capturingAuditLogger) Config() auditlog.Config {
	return l.config
}

func (l *capturingAuditLogger) Close() error {
	return nil
}

type erroringWriter struct {
	err error
}

func (w *erroringWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type batchRequestPreparerStub struct {
	capturedCtx      context.Context
	capturedProvider string
	capturedReq      *core.BatchRequest
	result           *core.BatchRewriteResult
	err              error
}

func (p *batchRequestPreparerStub) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	p.capturedCtx = ctx
	p.capturedProvider = providerType
	p.capturedReq = req
	if p.err != nil {
		return nil, p.err
	}
	return p.result, nil
}

type failingBatchStore struct {
	createErr error
}

func (s *failingBatchStore) Create(context.Context, *batchstore.StoredBatch) error {
	return s.createErr
}

func (s *failingBatchStore) Get(context.Context, string) (*batchstore.StoredBatch, error) {
	return nil, batchstore.ErrNotFound
}

func (s *failingBatchStore) List(context.Context, int, string) ([]*batchstore.StoredBatch, error) {
	return nil, nil
}

func (s *failingBatchStore) Update(context.Context, *batchstore.StoredBatch) error {
	return batchstore.ErrNotFound
}

func (s *failingBatchStore) Close() error {
	return nil
}

// mockProvider implements core.RoutableProvider for testing
type mockProvider struct {
	err               error
	response          *core.ChatResponse
	responsesResponse *core.ResponsesResponse
	modelsResponse    *core.ModelsResponse
	embeddingResponse *core.EmbeddingResponse
	embeddingErr      error
	streamData        string
	supportedModels   []string
	providerTypes     map[string]string
	providerNames     map[string]string

	batchCreateResponse         *core.BatchResponse
	batchCreateHints            map[string]string
	batchGetResponse            *core.BatchResponse
	batchCancelResponse         *core.BatchResponse
	batchResults                *core.BatchResultsResponse
	batchResultsHinted          *core.BatchResultsResponse
	batchResultsErr             error
	batchErr                    error
	capturedBatchReq            *core.BatchRequest
	capturedBatchCtx            context.Context
	capturedBatchProvider       string
	capturedBatchCancelProvider string
	capturedBatchCancelID       string
	capturedBatchHints          map[string]string
	capturedBatchHintsCtx       context.Context
	capturedBatchHintsProvider  string
	capturedBatchHintsBatchID   string
	clearedBatchHintProvider    string
	clearedBatchHintID          string

	fileCreateResponse      *core.FileObject
	fileCreateResponses     []*core.FileObject
	capturedFileCreateReqs  []*core.FileCreateRequest
	capturedFileDeleteIDs   []string
	fileGetResponse         *core.FileObject
	fileDeleteResponse      *core.FileDeleteResponse
	fileListResponse        *core.FileListResponse
	fileContentResponse     *core.FileContentResponse
	fileErr                 error
	fileListByProvider      map[string]*core.FileListResponse
	fileListPagesByProvider map[string]map[string]*core.FileListResponse
	fileListCalls           []fileListCall
	fileErrByProvider       map[string]error
	fileGetByProvider       map[string]*core.FileObject
	fileContentByProv       map[string]*core.FileContentResponse

	passthroughResponse     *core.PassthroughResponse
	passthroughErr          error
	lastPassthroughProvider string
	lastPassthroughReq      *core.PassthroughRequest

	responseGetResponse         *core.ResponsesResponse
	responseInputItemsResponse  *core.ResponseInputItemListResponse
	responseCancelResponse      *core.ResponsesResponse
	responseDeleteResponse      *core.ResponseDeleteResponse
	responseInputTokensResponse *core.ResponseInputTokensResponse
	responseCompactResponse     *core.ResponseCompactResponse
	responseLifecycleErr        error
	responseUtilityErr          error
	responseGetCalls            []responseCall
	responseInputItemsCalls     []responseCall
	responseCancelCalls         []responseCall
	responseDeleteCalls         []responseCall
	capturedResponseUtilityReqs []*core.ResponsesRequest
	capturedResponseUtility     []responseUtilityCall
}

type responseCall struct {
	provider string
	id       string
}

type responseUtilityCall struct {
	provider  string
	operation string
}

type fileListCall struct {
	provider string
	purpose  string
	limit    int
	after    string
}

type recordingModelAuthorizer struct {
	lastSelector core.ModelSelector
	err          error
	allow        func(core.ModelSelector) bool
}

func (a *recordingModelAuthorizer) ValidateModelAccess(_ context.Context, selector core.ModelSelector) error {
	a.lastSelector = selector
	return a.err
}

func (a *recordingModelAuthorizer) AllowsModel(_ context.Context, selector core.ModelSelector) bool {
	if a.allow != nil {
		return a.allow(selector)
	}
	return true
}

func (a *recordingModelAuthorizer) FilterPublicModels(_ context.Context, models []core.Model) []core.Model {
	return models
}

type staticExposedModelLister struct {
	models []core.Model
}

func (l staticExposedModelLister) ExposedModels() []core.Model {
	return append([]core.Model(nil), l.models...)
}

func readPassthroughRequestBody(t *testing.T, body io.ReadCloser) string {
	t.Helper()
	if body == nil {
		return ""
	}
	defer func() {
		_ = body.Close()
	}()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read passthrough request body: %v", err)
	}
	return string(data)
}

func (m *mockProvider) Supports(model string) bool {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil {
		model = selector.Model
	}
	return slices.Contains(m.supportedModels, model)
}

func (m *mockProvider) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil && selector.Provider != "" {
		if m.providerTypes != nil {
			if providerType, ok := m.providerTypes[selector.QualifiedModel()]; ok {
				return providerType
			}
		}
		model = selector.Model
	}

	if m.providerTypes != nil {
		if providerType, ok := m.providerTypes[model]; ok {
			return providerType
		}
		if providerType, ok := inferQualifiedProviderValue(m.providerTypes, model); ok {
			return providerType
		}
	}
	if m.Supports(model) {
		return "mock"
	}
	return ""
}

func (m *mockProvider) GetProviderName(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil && selector.Provider != "" {
		if m.providerNames != nil {
			if providerName, ok := m.providerNames[selector.QualifiedModel()]; ok {
				return providerName
			}
		}
		model = selector.Model
	}

	if m.providerNames != nil {
		if providerName, ok := m.providerNames[model]; ok {
			return providerName
		}
		if providerName, ok := inferQualifiedProviderValue(m.providerNames, model); ok {
			return providerName
		}
	}
	return ""
}

func (m *mockProvider) GetProviderNameForType(providerType string) string {
	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return ""
	}
	if len(m.providerNames) == 0 {
		return ""
	}
	for qualifiedModel, providerName := range m.providerNames {
		if strings.TrimSpace(m.providerTypes[qualifiedModel]) == providerType {
			return providerName
		}
	}
	return ""
}

func (m *mockProvider) GetProviderTypeForName(providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" || len(m.providerNames) == 0 {
		return ""
	}
	for qualifiedModel, candidate := range m.providerNames {
		if strings.TrimSpace(candidate) != providerName {
			continue
		}
		if providerType := strings.TrimSpace(m.providerTypes[qualifiedModel]); providerType != "" {
			return providerType
		}
	}
	return ""
}

func inferQualifiedProviderValue(values map[string]string, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}

	match := ""
	for qualifiedModel, value := range values {
		selector, err := core.ParseModelSelector(qualifiedModel, "")
		if err != nil || selector.Provider == "" || selector.Model != model || strings.TrimSpace(value) == "" {
			continue
		}
		if match != "" && match != value {
			return "", false
		}
		match = value
	}
	if match == "" {
		return "", false
	}
	return match, true
}

func (m *mockProvider) NativeFileProviderTypes() []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(m.providerTypes))
	for _, providerType := range m.providerTypes {
		if providerType == "" {
			continue
		}
		if _, exists := seen[providerType]; exists {
			continue
		}
		seen[providerType] = struct{}{}
		result = append(result, providerType)
	}
	sort.Strings(result)
	return result
}

func (m *mockProvider) NativeResponseProviderTypes() []string {
	return m.NativeFileProviderTypes()
}

type providerWithoutFileInventory struct {
	inner *mockProvider
}

func (p *providerWithoutFileInventory) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.inner.ChatCompletion(ctx, req)
}

func (p *providerWithoutFileInventory) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.inner.StreamChatCompletion(ctx, req)
}

func (p *providerWithoutFileInventory) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.inner.ListModels(ctx)
}

func (p *providerWithoutFileInventory) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.inner.Responses(ctx, req)
}

func (p *providerWithoutFileInventory) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.inner.StreamResponses(ctx, req)
}

func (p *providerWithoutFileInventory) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.inner.Embeddings(ctx, req)
}

func (p *providerWithoutFileInventory) Supports(model string) bool {
	return p.inner.Supports(model)
}

func (p *providerWithoutFileInventory) GetProviderType(model string) string {
	return p.inner.GetProviderType(model)
}

func (p *providerWithoutFileInventory) CreateFile(ctx context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	return p.inner.CreateFile(ctx, providerType, req)
}

func (p *providerWithoutFileInventory) ListFiles(ctx context.Context, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	return p.inner.ListFiles(ctx, providerType, purpose, limit, after)
}

func (p *providerWithoutFileInventory) GetFile(ctx context.Context, providerType, id string) (*core.FileObject, error) {
	return p.inner.GetFile(ctx, providerType, id)
}

func (p *providerWithoutFileInventory) DeleteFile(ctx context.Context, providerType, id string) (*core.FileDeleteResponse, error) {
	return p.inner.DeleteFile(ctx, providerType, id)
}

func (p *providerWithoutFileInventory) GetFileContent(ctx context.Context, providerType, id string) (*core.FileContentResponse, error) {
	return p.inner.GetFileContent(ctx, providerType, id)
}

type providerWithoutResponseLifecycle struct {
	inner *mockProvider
}

func (p *providerWithoutResponseLifecycle) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.inner.ChatCompletion(ctx, req)
}

func (p *providerWithoutResponseLifecycle) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.inner.StreamChatCompletion(ctx, req)
}

func (p *providerWithoutResponseLifecycle) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.inner.ListModels(ctx)
}

func (p *providerWithoutResponseLifecycle) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.inner.Responses(ctx, req)
}

func (p *providerWithoutResponseLifecycle) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.inner.StreamResponses(ctx, req)
}

func (p *providerWithoutResponseLifecycle) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.inner.Embeddings(ctx, req)
}

func (p *providerWithoutResponseLifecycle) Supports(model string) bool {
	return p.inner.Supports(model)
}

func (p *providerWithoutResponseLifecycle) GetProviderType(model string) string {
	return p.inner.GetProviderType(model)
}

func (m *mockProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(strings.NewReader(m.streamData)), nil
}

func (m *mockProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.modelsResponse, nil
}

func (m *mockProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.responsesResponse, nil
}

func (m *mockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(strings.NewReader(m.streamData)), nil
}

func (m *mockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if m.embeddingErr != nil {
		return nil, m.embeddingErr
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.embeddingResponse, nil
}

func (m *mockProvider) Passthrough(_ context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	m.lastPassthroughProvider = providerType
	m.lastPassthroughReq = req
	if m.passthroughErr != nil {
		return nil, m.passthroughErr
	}
	return m.passthroughResponse, nil
}

func (m *mockProvider) GetResponse(_ context.Context, providerType, id string, _ core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	m.responseGetCalls = append(m.responseGetCalls, responseCall{provider: providerType, id: id})
	if m.responseLifecycleErr != nil {
		return nil, m.responseLifecycleErr
	}
	if m.responseGetResponse != nil {
		return m.responseGetResponse, nil
	}
	return &core.ResponsesResponse{ID: id, Object: "response", Model: "gpt-5-mini", Provider: providerType, Status: "completed"}, nil
}

func (m *mockProvider) ListResponseInputItems(_ context.Context, providerType, id string, _ core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	m.responseInputItemsCalls = append(m.responseInputItemsCalls, responseCall{provider: providerType, id: id})
	if m.responseLifecycleErr != nil {
		return nil, m.responseLifecycleErr
	}
	if m.responseInputItemsResponse != nil {
		return m.responseInputItemsResponse, nil
	}
	return &core.ResponseInputItemListResponse{Object: "list"}, nil
}

func (m *mockProvider) CancelResponse(_ context.Context, providerType, id string) (*core.ResponsesResponse, error) {
	m.responseCancelCalls = append(m.responseCancelCalls, responseCall{provider: providerType, id: id})
	if m.responseLifecycleErr != nil {
		return nil, m.responseLifecycleErr
	}
	if m.responseCancelResponse != nil {
		return m.responseCancelResponse, nil
	}
	return &core.ResponsesResponse{ID: id, Object: "response", Model: "gpt-5-mini", Provider: providerType, Status: "cancelled"}, nil
}

func (m *mockProvider) DeleteResponse(_ context.Context, providerType, id string) (*core.ResponseDeleteResponse, error) {
	m.responseDeleteCalls = append(m.responseDeleteCalls, responseCall{provider: providerType, id: id})
	if m.responseLifecycleErr != nil {
		return nil, m.responseLifecycleErr
	}
	if m.responseDeleteResponse != nil {
		return m.responseDeleteResponse, nil
	}
	return &core.ResponseDeleteResponse{ID: id, Object: "response", Deleted: true}, nil
}

func (m *mockProvider) CountResponseInputTokens(_ context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseInputTokensResponse, error) {
	m.capturedResponseUtilityReqs = append(m.capturedResponseUtilityReqs, req)
	m.capturedResponseUtility = append(m.capturedResponseUtility, responseUtilityCall{
		provider:  providerType,
		operation: "CountResponseInputTokens",
	})
	if m.responseUtilityErr != nil {
		return nil, m.responseUtilityErr
	}
	if m.responseInputTokensResponse != nil {
		return m.responseInputTokensResponse, nil
	}
	return &core.ResponseInputTokensResponse{Object: "response.input_tokens", InputTokens: 7}, nil
}

func (m *mockProvider) CompactResponse(_ context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseCompactResponse, error) {
	m.capturedResponseUtilityReqs = append(m.capturedResponseUtilityReqs, req)
	m.capturedResponseUtility = append(m.capturedResponseUtility, responseUtilityCall{
		provider:  providerType,
		operation: "CompactResponse",
	})
	if m.responseUtilityErr != nil {
		return nil, m.responseUtilityErr
	}
	if m.responseCompactResponse != nil {
		return m.responseCompactResponse, nil
	}
	return &core.ResponseCompactResponse{ID: "cmp_1", Object: "response.compaction", Provider: providerType}, nil
}

func (m *mockProvider) CreateBatch(_ context.Context, _ string, req *core.BatchRequest) (*core.BatchResponse, error) {
	m.capturedBatchReq = req
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	if m.batchCreateResponse == nil {
		now := int64(1000)
		return &core.BatchResponse{
			ID:            "provider-batch-1",
			Object:        "batch",
			Status:        "in_progress",
			CreatedAt:     now,
			RequestCounts: core.BatchRequestCounts{Total: 1, Completed: 0, Failed: 0},
		}, nil
	}
	return m.batchCreateResponse, nil
}

func (m *mockProvider) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	m.capturedBatchCtx = ctx
	m.capturedBatchProvider = providerType
	resp, err := m.CreateBatch(ctx, providerType, req)
	if err != nil {
		return nil, nil, err
	}
	if len(m.batchCreateHints) == 0 {
		return resp, nil, nil
	}
	hints := make(map[string]string, len(m.batchCreateHints))
	maps.Copy(hints, m.batchCreateHints)
	return resp, hints, nil
}

func (m *mockProvider) GetBatch(_ context.Context, _ string, _ string) (*core.BatchResponse, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	if m.batchGetResponse != nil {
		return m.batchGetResponse, nil
	}
	return m.batchCreateResponse, nil
}

func (m *mockProvider) ListBatches(_ context.Context, _ string, _ int, _ string) (*core.BatchListResponse, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	return &core.BatchListResponse{Object: "list"}, nil
}

func (m *mockProvider) CancelBatch(_ context.Context, providerType, batchID string) (*core.BatchResponse, error) {
	m.capturedBatchCancelProvider = providerType
	m.capturedBatchCancelID = batchID
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	if m.batchCancelResponse != nil {
		return m.batchCancelResponse, nil
	}
	return &core.BatchResponse{
		ID:     "provider-batch-1",
		Object: "batch",
		Status: "cancelled",
	}, nil
}

func (m *mockProvider) GetBatchResults(_ context.Context, _ string, _ string) (*core.BatchResultsResponse, error) {
	if m.batchResultsErr != nil {
		return nil, m.batchResultsErr
	}
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	if m.batchResults != nil {
		return m.batchResults, nil
	}
	return &core.BatchResultsResponse{
		Object:  "list",
		BatchID: "provider-batch-1",
		Data: []core.BatchResultItem{
			{Index: 0, StatusCode: 200},
		},
	}, nil
}

func (m *mockProvider) GetBatchResultsWithHints(ctx context.Context, providerType string, batchID string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	m.capturedBatchHintsCtx = ctx
	m.capturedBatchHintsProvider = providerType
	m.capturedBatchHintsBatchID = batchID
	if len(endpointByCustomID) > 0 {
		m.capturedBatchHints = make(map[string]string, len(endpointByCustomID))
		maps.Copy(m.capturedBatchHints, endpointByCustomID)
	}
	if m.batchResultsHinted != nil {
		return m.batchResultsHinted, nil
	}
	return m.GetBatchResults(context.Background(), "", "")
}

func (m *mockProvider) ClearBatchResultHints(providerType string, batchID string) {
	m.clearedBatchHintProvider = providerType
	m.clearedBatchHintID = batchID
}

func (m *mockProvider) CreateFile(_ context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	if m.fileErr != nil {
		return nil, m.fileErr
	}
	copy := *req
	if req.ContentReader != nil {
		content, err := io.ReadAll(req.ContentReader)
		if err != nil {
			return nil, err
		}
		copy.Content = content
		copy.ContentReader = nil
	} else {
		copy.Content = append([]byte(nil), req.Content...)
	}
	m.capturedFileCreateReqs = append(m.capturedFileCreateReqs, &copy)
	if len(m.fileCreateResponses) > 0 {
		resp := m.fileCreateResponses[0]
		m.fileCreateResponses = m.fileCreateResponses[1:]
		return resp, nil
	}
	if m.fileCreateResponse != nil {
		return m.fileCreateResponse, nil
	}
	return &core.FileObject{
		ID:        "file_mock_1",
		Object:    "file",
		Bytes:     int64(len(copy.Content)),
		CreatedAt: 1000,
		Filename:  req.Filename,
		Purpose:   req.Purpose,
		Provider:  providerType,
	}, nil
}

func (m *mockProvider) ListFiles(_ context.Context, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	m.fileListCalls = append(m.fileListCalls, fileListCall{
		provider: providerType,
		purpose:  purpose,
		limit:    limit,
		after:    after,
	})
	if m.fileErrByProvider != nil {
		if err, ok := m.fileErrByProvider[providerType]; ok && err != nil {
			return nil, err
		}
	}
	if m.fileErr != nil {
		return nil, m.fileErr
	}
	if m.fileListPagesByProvider != nil {
		if byCursor, ok := m.fileListPagesByProvider[providerType]; ok {
			if resp, ok := byCursor[after]; ok {
				return resp, nil
			}
		}
	}
	if m.fileListByProvider != nil {
		if resp, ok := m.fileListByProvider[providerType]; ok {
			return resp, nil
		}
	}
	if m.fileListResponse != nil {
		return m.fileListResponse, nil
	}
	return &core.FileListResponse{
		Object: "list",
		Data: []core.FileObject{
			{
				ID:        "file_mock_1",
				Object:    "file",
				Bytes:     10,
				CreatedAt: 1000,
				Filename:  "a.jsonl",
				Purpose:   firstNonEmpty(purpose, "batch"),
				Provider:  providerType,
			},
		},
	}, nil
}

func (m *mockProvider) GetFile(_ context.Context, providerType, id string) (*core.FileObject, error) {
	if m.fileErrByProvider != nil {
		if err, ok := m.fileErrByProvider[providerType]; ok && err != nil {
			return nil, err
		}
	}
	if m.fileErr != nil {
		return nil, m.fileErr
	}
	if m.fileGetByProvider != nil {
		if resp, ok := m.fileGetByProvider[providerType]; ok {
			return resp, nil
		}
	}
	if m.fileGetResponse != nil {
		return m.fileGetResponse, nil
	}
	return &core.FileObject{
		ID:        id,
		Object:    "file",
		Bytes:     10,
		CreatedAt: 1000,
		Filename:  "a.jsonl",
		Purpose:   "batch",
		Provider:  providerType,
	}, nil
}

func (m *mockProvider) DeleteFile(_ context.Context, providerType string, id string) (*core.FileDeleteResponse, error) {
	if m.fileErrByProvider != nil {
		if err, ok := m.fileErrByProvider[providerType]; ok && err != nil {
			return nil, err
		}
	}
	if m.fileErr != nil {
		return nil, m.fileErr
	}
	m.capturedFileDeleteIDs = append(m.capturedFileDeleteIDs, id)
	if m.fileDeleteResponse != nil {
		return m.fileDeleteResponse, nil
	}
	return &core.FileDeleteResponse{ID: id, Object: "file", Deleted: true}, nil
}

func (m *mockProvider) GetFileContent(_ context.Context, providerType string, id string) (*core.FileContentResponse, error) {
	if m.fileErrByProvider != nil {
		if err, ok := m.fileErrByProvider[providerType]; ok && err != nil {
			return nil, err
		}
	}
	if m.fileErr != nil {
		return nil, m.fileErr
	}
	if m.fileContentByProv != nil {
		if resp, ok := m.fileContentByProv[providerType]; ok {
			return resp, nil
		}
	}
	if m.fileContentResponse != nil {
		return m.fileContentResponse, nil
	}
	return &core.FileContentResponse{
		ID:          id,
		ContentType: "application/jsonl",
		Data:        []byte("{\"ok\":true}\n"),
	}, nil
}

func TestChatCompletion(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		response: &core.ChatResponse{
			ID:      "chatcmpl-123",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4o-mini",
			Choices: []core.Choice{
				{
					Index:        0,
					Message:      core.ResponseMessage{Role: "assistant", Content: "Hello!"},
					FinishReason: "stop",
				},
			},
			Usage: core.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "chatcmpl-123") {
		t.Errorf("response missing expected ID, got: %s", body)
	}
	if !strings.Contains(body, "Hello!") {
		t.Errorf("response missing expected content, got: %s", body)
	}
}

func TestChatCompletion_BindsMultimodalContent(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
			response: &core.ChatResponse{
				ID:      "chatcmpl-123",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-4o-mini",
				Choices: []core.Choice{
					{
						Index:        0,
						Message:      core.ResponseMessage{Role: "assistant", Content: "ok"},
						FinishReason: "stop",
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}

	parts, ok := core.NormalizeContentParts(provider.capturedChatReq.Messages[0].Content)
	if !ok {
		t.Fatalf("captured content type = %T, want structured content", provider.capturedChatReq.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Describe this image" {
		t.Fatalf("unexpected first part: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/image.png" {
		t.Fatalf("unexpected second part: %+v", parts[1])
	}
}

func TestChatCompletion_PreservesUnknownTopLevelFields(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-mini"},
			response: &core.ChatResponse{
				ID:      "chatcmpl-123",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-5-mini",
				Choices: []core.Choice{
					{
						Index:        0,
						Message:      core.ResponseMessage{Role: "assistant", Content: "ok"},
						FinishReason: "stop",
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{
		"model":"gpt-5-mini",
		"messages":[{"role":"user","content":"return json"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"math_response",
				"schema":{"type":"object","properties":{"answer":{"type":"string"}}}
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if provider.capturedChatReq.ExtraFields.Lookup("response_format") == nil {
		t.Fatal("response_format missing from ExtraFields")
	}

	body, err := json.Marshal(provider.capturedChatReq)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !bytes.Contains(body, []byte(`"response_format"`)) {
		t.Fatalf("marshaled request missing response_format: %s", string(body))
	}
}

func TestChatCompletion_PreservesUnknownNestedFields(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-mini"},
			response: &core.ChatResponse{
				ID:      "chatcmpl-123",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-5-mini",
				Choices: []core.Choice{
					{
						Index:        0,
						Message:      core.ResponseMessage{Role: "assistant", Content: "ok"},
						FinishReason: "stop",
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{
		"model":"gpt-5-mini",
		"messages":[
			{
				"role":"user",
				"name":"alice",
				"content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]
			}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if provider.capturedChatReq.Messages[0].ExtraFields.Lookup("name") == nil {
		t.Fatal("message.name missing from ExtraFields")
	}

	body, err := json.Marshal(provider.capturedChatReq)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	messages := decoded["messages"].([]any)
	firstMsg := messages[0].(map[string]any)
	if firstMsg["name"] != "alice" {
		t.Fatalf("messages[0].name = %#v, want alice", firstMsg["name"])
	}
	content := firstMsg["content"].([]any)
	firstPart := content[0].(map[string]any)
	if _, ok := firstPart["cache_control"].(map[string]any); !ok {
		t.Fatalf("messages[0].content[0].cache_control = %#v, want object", firstPart["cache_control"])
	}
}

func TestChatCompletion_UsesIngressFrameForDecoding(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-mini"},
			response: &core.ChatResponse{
				ID:      "chatcmpl-123",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-5-mini",
				Choices: []core.Choice{
					{
						Index:        0,
						Message:      core.ResponseMessage{Role: "assistant", Content: "ok"},
						FinishReason: "stop",
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-ingress-1")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-5-mini",
			"messages":[{"role":"user","content":"return json"}],
			"response_format":{"type":"json_schema"}
		}`),
		false,
		"req-ingress-1",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if provider.capturedChatReq.ExtraFields.Lookup("response_format") == nil {
		t.Fatal("response_format missing from ExtraFields")
	}

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedChatRequest() == nil {
		t.Fatalf("expected semantic envelope to cache ChatRequest, got %+v", env)
	}
	if env.CachedChatRequest() != provider.capturedChatReq {
		t.Fatal("cached ChatRequest does not match provider request")
	}
}

func TestChatCompletion_NormalizesSemanticSelectorHints(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-mini"},
			response: &core.ChatResponse{
				ID:     "chatcmpl_123",
				Object: "chat.completion",
				Model:  "gpt-5-mini",
				Choices: []core.Choice{
					{
						Index:        0,
						FinishReason: "stop",
						Message: core.ResponseMessage{
							Role:    "assistant",
							Content: "ok",
						},
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"openai/gpt-5-mini",
			"messages":[{"role":"user","content":"return json"}]
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if provider.capturedChatReq.Model != "gpt-5-mini" {
		t.Fatalf("captured model = %q, want gpt-5-mini", provider.capturedChatReq.Model)
	}
	if provider.capturedChatReq.Provider != "openai" {
		t.Fatalf("captured provider = %q, want openai", provider.capturedChatReq.Provider)
	}

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedChatRequest() == nil {
		t.Fatalf("expected semantic envelope to cache ChatRequest, got %+v", env)
	}
	if env.RouteHints.Model != "gpt-5-mini" {
		t.Fatalf("RouteHints.Model = %q, want gpt-5-mini", env.RouteHints.Model)
	}
	if env.RouteHints.Provider != "openai" {
		t.Fatalf("RouteHints.Provider = %q, want openai", env.RouteHints.Provider)
	}
}

func TestChatCompletion_ResolvesQualifiedMaskingAliasBeforeHandlerRouting(t *testing.T) {
	catalog := aliasesTestCatalog{
		supported: map[string]bool{
			"anthropic/claude-opus-4-6": true,
			"openai/gpt-5-nano":         true,
		},
		providerTypes: map[string]string{
			"anthropic/claude-opus-4-6": "anthropic",
			"openai/gpt-5-nano":         "openai",
		},
		models: map[string]core.Model{
			"anthropic/claude-opus-4-6": {ID: "claude-opus-4-6", Object: "model"},
			"openai/gpt-5-nano":         {ID: "gpt-5-nano", Object: "model"},
		},
	}

	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "anthropic/claude-opus-4-6", TargetModel: "gpt-5-nano", TargetProvider: "openai", Enabled: true},
	), &catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-nano"},
			providerTypes: map[string]string{
				"openai/gpt-5-nano": "openai",
			},
			response: &core.ChatResponse{
				ID:       "chatcmpl_alias_123",
				Object:   "chat.completion",
				Model:    "gpt-5-nano",
				Provider: "openai",
				Choices: []core.Choice{
					{
						Index:        0,
						FinishReason: "stop",
						Message: core.ResponseMessage{
							Role:    "assistant",
							Content: "ok",
						},
					},
				},
			},
		},
	}

	provider := aliases.NewProviderWithOptions(inner, service, aliases.Options{})

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"anthropic/claude-opus-4-6",
			"messages":[{"role":"user","content":"return json"}]
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if inner.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if inner.capturedChatReq.Model != "gpt-5-nano" {
		t.Fatalf("captured model = %q, want gpt-5-nano", inner.capturedChatReq.Model)
	}
	if inner.capturedChatReq.Provider != "openai" {
		t.Fatalf("captured provider = %q, want openai", inner.capturedChatReq.Provider)
	}

	workflow := core.GetWorkflow(c.Request().Context())
	if workflow == nil {
		t.Fatal("expected workflow in context")
	}
	if workflow.Mode != core.ExecutionModeTranslated {
		t.Fatalf("workflow mode = %q, want %q", workflow.Mode, core.ExecutionModeTranslated)
	}
	if workflow.ProviderType != "openai" {
		t.Fatalf("workflow provider type = %q, want openai", workflow.ProviderType)
	}
	if workflow.Resolution == nil {
		t.Fatal("expected workflow resolution")
	}
	if !workflow.Resolution.AliasApplied {
		t.Fatal("expected alias resolution to be marked as applied")
	}
	if workflow.ResolvedQualifiedModel() != "openai/gpt-5-nano" {
		t.Fatalf("workflow resolved model = %q, want openai/gpt-5-nano", workflow.ResolvedQualifiedModel())
	}
}

func TestChatCompletion_UsesExplicitAliasResolverWithoutProviderDecorator(t *testing.T) {
	catalog := aliasesTestCatalog{
		supported: map[string]bool{
			"anthropic/claude-opus-4-6": true,
			"openai/gpt-5-nano":         true,
		},
		providerTypes: map[string]string{
			"anthropic/claude-opus-4-6": "anthropic",
			"openai/gpt-5-nano":         "openai",
		},
		models: map[string]core.Model{
			"anthropic/claude-opus-4-6": {ID: "claude-opus-4-6", Object: "model"},
			"openai/gpt-5-nano":         {ID: "gpt-5-nano", Object: "model"},
		},
	}

	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "anthropic/claude-opus-4-6", TargetModel: "gpt-5-nano", TargetProvider: "openai", Enabled: true},
	), &catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-nano"},
			providerTypes: map[string]string{
				"openai/gpt-5-nano": "openai",
			},
			response: &core.ChatResponse{
				ID:       "chatcmpl_alias_resolver_123",
				Object:   "chat.completion",
				Model:    "gpt-5-nano",
				Provider: "openai",
				Choices: []core.Choice{
					{
						Index:        0,
						FinishReason: "stop",
						Message: core.ResponseMessage{
							Role:    "assistant",
							Content: "ok",
						},
					},
				},
			},
		},
	}

	e := echo.New()
	handler := newHandler(inner, nil, nil, nil, service, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"anthropic/claude-opus-4-6",
			"messages":[{"role":"user","content":"return json"}]
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if inner.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if inner.capturedChatReq.Model != "gpt-5-nano" {
		t.Fatalf("captured model = %q, want gpt-5-nano", inner.capturedChatReq.Model)
	}
	if inner.capturedChatReq.Provider != "openai" {
		t.Fatalf("captured provider = %q, want openai", inner.capturedChatReq.Provider)
	}

	workflow := core.GetWorkflow(c.Request().Context())
	if workflow == nil || workflow.Resolution == nil {
		t.Fatal("expected workflow resolution in context")
	}
	if !workflow.Resolution.AliasApplied {
		t.Fatal("expected alias resolution to be marked as applied")
	}
	if workflow.ResolvedQualifiedModel() != "openai/gpt-5-nano" {
		t.Fatalf("workflow resolved model = %q, want openai/gpt-5-nano", workflow.ResolvedQualifiedModel())
	}
}

func TestChatCompletion_UsesExplicitAliasResolverWithAliasProviderInventoryOnly(t *testing.T) {
	catalog := aliasesTestCatalog{
		supported: map[string]bool{
			"anthropic/claude-opus-4-6": true,
			"openai/gpt-5-nano":         true,
		},
		providerTypes: map[string]string{
			"anthropic/claude-opus-4-6": "anthropic",
			"openai/gpt-5-nano":         "openai",
		},
		models: map[string]core.Model{
			"anthropic/claude-opus-4-6": {ID: "claude-opus-4-6", Object: "model"},
			"openai/gpt-5-nano":         {ID: "gpt-5-nano", Object: "model"},
		},
	}

	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "anthropic/claude-opus-4-6", TargetModel: "gpt-5-nano", TargetProvider: "openai", Enabled: true},
	), &catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-nano"},
			providerTypes: map[string]string{
				"openai/gpt-5-nano": "openai",
			},
			response: &core.ChatResponse{
				ID:       "chatcmpl_alias_inventory_123",
				Object:   "chat.completion",
				Model:    "gpt-5-nano",
				Provider: "openai",
				Choices: []core.Choice{
					{
						Index:        0,
						FinishReason: "stop",
						Message: core.ResponseMessage{
							Role:    "assistant",
							Content: "ok",
						},
					},
				},
			},
		},
	}
	provider := aliases.NewProviderWithOptions(inner, service, aliases.Options{
		DisableTranslatedRequestProcessing: true,
		DisableNativeBatchPreparation:      true,
	})

	e := echo.New()
	handler := newHandler(provider, nil, nil, nil, service, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"anthropic/claude-opus-4-6",
			"messages":[{"role":"user","content":"return json"}]
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if inner.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if inner.capturedChatReq.Model != "gpt-5-nano" {
		t.Fatalf("captured model = %q, want gpt-5-nano", inner.capturedChatReq.Model)
	}
	if inner.capturedChatReq.Provider != "openai" {
		t.Fatalf("captured provider = %q, want openai", inner.capturedChatReq.Provider)
	}
}

func TestChatCompletion_UsesExplicitTranslatedRequestPatcher(t *testing.T) {
	pipeline := guardrails.NewPipeline()
	systemPrompt, err := guardrails.NewSystemPromptGuardrail("test", guardrails.SystemPromptInject, "guardrail system")
	if err != nil {
		t.Fatalf("NewSystemPromptGuardrail() error = %v", err)
	}
	pipeline.Add(systemPrompt, 0)

	inner := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-nano"},
			providerTypes: map[string]string{
				"gpt-5-nano": "mock",
			},
			response: &core.ChatResponse{
				ID:       "chatcmpl_guardrail_123",
				Object:   "chat.completion",
				Model:    "gpt-5-nano",
				Provider: "mock",
				Choices: []core.Choice{
					{
						Index:        0,
						FinishReason: "stop",
						Message: core.ResponseMessage{
							Role:    "assistant",
							Content: "ok",
						},
					},
				},
			},
		},
	}

	patcher := guardrails.NewRequestPatcher(pipeline)

	e := echo.New()
	handler := newHandler(inner, nil, nil, nil, nil, nil, nil, patcher)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-5-nano",
			"messages":[{"role":"user","content":"return json"}]
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if inner.capturedChatReq == nil {
		t.Fatal("expected chat request to be captured")
	}
	if len(inner.capturedChatReq.Messages) != 2 {
		t.Fatalf("captured messages = %d, want 2", len(inner.capturedChatReq.Messages))
	}
	if inner.capturedChatReq.Messages[0].Role != "system" || inner.capturedChatReq.Messages[0].Content != "guardrail system" {
		t.Fatalf("first message = %+v, want injected guardrail system prompt", inner.capturedChatReq.Messages[0])
	}
	if inner.capturedChatReq.Messages[1].Role != "user" {
		t.Fatalf("second message role = %q, want user", inner.capturedChatReq.Messages[1].Role)
	}
}

func TestBatches_UsesExplicitGuardrailBatchPreparer(t *testing.T) {
	pipeline := guardrails.NewPipeline()
	systemPrompt, err := guardrails.NewSystemPromptGuardrail("test", guardrails.SystemPromptInject, "guardrail system")
	if err != nil {
		t.Fatalf("NewSystemPromptGuardrail() error = %v", err)
	}
	pipeline.Add(systemPrompt, 0)

	mock := &mockProvider{
		supportedModels: []string{"gpt-5-nano"},
		providerTypes: map[string]string{
			"gpt-5-nano": "mock",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:            "provider-batch-guardrail-123",
			Object:        "batch",
			Status:        "in_progress",
			Endpoint:      "/v1/chat/completions",
			CreatedAt:     1234567890,
			RequestCounts: core.BatchRequestCounts{Total: 1},
		},
	}
	batchPreparer := guardrails.NewBatchPreparer(mock, pipeline)

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.batchRequestPreparer = batchPreparer

	reqBody := `{
	  "completion_window":"24h",
	  "requests":[
	    {
	      "custom_id":"chat-1",
	      "method":"POST",
	      "url":"/v1/chat/completions",
	      "body":{"model":"gpt-5-nano","messages":[{"role":"user","content":"Hi"}]}
	    }
	  ]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.Batches(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if mock.capturedBatchReq == nil || len(mock.capturedBatchReq.Requests) != 1 {
		t.Fatalf("capturedBatchReq = %#v, want one request", mock.capturedBatchReq)
	}

	var chatReq core.ChatRequest
	if err := json.Unmarshal(mock.capturedBatchReq.Requests[0].Body, &chatReq); err != nil {
		t.Fatalf("failed to decode rewritten batch item: %v", err)
	}
	if len(chatReq.Messages) != 2 {
		t.Fatalf("rewritten batch messages = %d, want 2", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "system" || chatReq.Messages[0].Content != "guardrail system" {
		t.Fatalf("first batch message = %+v, want injected guardrail system prompt", chatReq.Messages[0])
	}
}

func TestResponses_UsesIngressFrameForDecoding(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-mini"},
			responsesResponse: &core.ResponsesResponse{
				ID:        "resp_123",
				Object:    "response",
				CreatedAt: 1234567890,
				Model:     "gpt-5-mini",
				Status:    "completed",
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/responses",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-5-mini",
			"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}]
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedResponsesReq == nil {
		t.Fatal("expected responses request to be captured")
	}
	input, ok := provider.capturedResponsesReq.Input.([]core.ResponsesInputElement)
	if !ok || len(input) != 1 {
		t.Fatalf("captured input = %#v, want []ResponsesInputElement len=1", provider.capturedResponsesReq.Input)
	}
	if input[0].ExtraFields.Lookup("x_trace") == nil {
		t.Fatal("input[0].x_trace missing from ExtraFields")
	}

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedResponsesRequest() == nil {
		t.Fatalf("expected semantic envelope to cache ResponsesRequest, got %+v", env)
	}
	if env.CachedResponsesRequest() != provider.capturedResponsesReq {
		t.Fatal("cached ResponsesRequest does not match provider request")
	}
}

func TestEmbeddings_UsesIngressFrameForDecoding(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"text-embedding-3-large"},
			embeddingResponse: &core.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-3-large",
				Data: []core.EmbeddingData{
					{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2]`), Index: 0},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/embeddings",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"text-embedding-3-large",
			"input":"hello",
			"x_meta":{"trace":"abc"}
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Embeddings(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedEmbeddingReq == nil {
		t.Fatal("expected embeddings request to be captured")
	}
	if provider.capturedEmbeddingReq.ExtraFields.Lookup("x_meta") == nil {
		t.Fatalf("x_meta missing from ExtraFields: %+v", provider.capturedEmbeddingReq.ExtraFields)
	}

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedEmbeddingRequest() == nil {
		t.Fatalf("expected semantic envelope to cache EmbeddingRequest, got %+v", env)
	}
	if env.CachedEmbeddingRequest() != provider.capturedEmbeddingReq {
		t.Fatal("cached EmbeddingRequest does not match provider request")
	}
}

func TestBatches_UsesIngressFrameForDecoding(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		batchCreateResponse: &core.BatchResponse{
			ID:               "provider-batch-123",
			Object:           "batch",
			Status:           "in_progress",
			Endpoint:         "/v1/chat/completions",
			CompletionWindow: "24h",
			CreatedAt:        1234567890,
			RequestCounts: core.BatchRequestCounts{
				Total: 1,
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/batches", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/batches",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"completion_window":"24h",
			"requests":[{
				"custom_id":"chat-1",
				"method":"POST",
				"url":"/v1/chat/completions",
				"body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]},
				"x_item_flag":{"enabled":true}
			}],
			"x_top":{"trace":"batch-1"}
		}`),
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Batches(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if mock.capturedBatchReq == nil {
		t.Fatal("expected batch request to be captured")
	}
	if mock.capturedBatchReq.ExtraFields.Lookup("x_top") == nil {
		t.Fatalf("x_top missing from ExtraFields: %+v", mock.capturedBatchReq.ExtraFields)
	}
	if len(mock.capturedBatchReq.Requests) != 1 {
		t.Fatalf("len(Requests) = %d, want 1", len(mock.capturedBatchReq.Requests))
	}
	if mock.capturedBatchReq.Requests[0].ExtraFields.Lookup("x_item_flag") == nil {
		t.Fatalf("x_item_flag missing from item ExtraFields: %+v", mock.capturedBatchReq.Requests[0].ExtraFields)
	}

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedBatchRequest() == nil {
		t.Fatalf("expected semantic envelope to cache BatchRequest, got %+v", env)
	}
	if env.CachedBatchRequest() != mock.capturedBatchReq {
		t.Fatal("cached BatchRequest does not match provider request")
	}
}

func TestGetBatch_UsesSemanticEnvelopeRouteMetadata(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		batchCreateResponse: &core.BatchResponse{
			ID:               "provider-batch-123",
			Object:           "batch",
			Status:           "in_progress",
			Endpoint:         "/v1/chat/completions",
			CompletionWindow: "24h",
			CreatedAt:        1234567890,
			RequestCounts:    core.BatchRequestCounts{Total: 1},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
		"endpoint":"/v1/chat/completions",
		"requests":[{"custom_id":"chat-1","method":"POST","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}}]
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	require.NoError(t, handler.Batches(createCtx))

	var created core.BatchResponse
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	getReq := httptest.NewRequest(http.MethodGet, "/v1/batches/wrong-id", nil)
	frame := core.NewRequestSnapshot(http.MethodGet, "/v1/batches/"+created.ID, map[string]string{"id": created.ID}, nil, nil, "", nil, false, "", nil)
	getReq = withRequestSnapshotAndPrompt(getReq, frame)

	getRec := httptest.NewRecorder()
	getCtx := e.NewContext(getReq, getRec)
	getCtx.SetPath("/v1/batches/:id")
	setPathParam(getCtx, "id", "wrong-id")

	require.NoError(t, handler.GetBatch(getCtx))
	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Contains(t, getRec.Body.String(), created.ID)
}

func TestListBatches_UsesSemanticEnvelopeQueryMetadata(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		batchCreateResponse: &core.BatchResponse{
			ID:               "provider-batch-123",
			Object:           "batch",
			Status:           "in_progress",
			Endpoint:         "/v1/chat/completions",
			CompletionWindow: "24h",
			CreatedAt:        1234567890,
			RequestCounts:    core.BatchRequestCounts{Total: 1},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
		"endpoint":"/v1/chat/completions",
		"requests":[{"custom_id":"chat-1","method":"POST","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}}]
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	require.NoError(t, handler.Batches(createCtx))

	listReq := httptest.NewRequest(http.MethodGet, "/v1/batches?limit=bad", nil)
	frame := core.NewRequestSnapshot(
		http.MethodGet,
		"/v1/batches",
		nil,
		map[string][]string{
			"limit": {"1"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)
	listReq = withRequestSnapshotAndPrompt(listReq, frame)

	listRec := httptest.NewRecorder()
	listCtx := e.NewContext(listReq, listRec)

	require.NoError(t, handler.ListBatches(listCtx))
	require.Equal(t, http.StatusOK, listRec.Code)

	var listResp core.BatchListResponse
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listResp))
	require.Len(t, listResp.Data, 1)
}

func TestChatCompletionStreaming(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]

`
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		streamData:      streamData,
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "stream": true, "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("response should contain SSE data, got: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("response should contain [DONE], got: %s", body)
	}
}

func TestChatCompletionStreaming_FastPathUsesPassthroughForOpenAICompatibleProviders(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-123\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\ndata: [DONE]\n\n"
	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(streamData)),
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Body.String(); got != streamData {
		t.Fatalf("stream body = %q, want %q", got, streamData)
	}
	if mock.lastPassthroughProvider != "openai" {
		t.Fatalf("lastPassthroughProvider = %q, want openai", mock.lastPassthroughProvider)
	}
	if mock.lastPassthroughReq == nil {
		t.Fatal("lastPassthroughReq = nil, want passthrough request")
	}
	if body := readPassthroughRequestBody(t, mock.lastPassthroughReq.Body); body != reqBody {
		t.Fatalf("passthrough body = %q, want %q", body, reqBody)
	}
}

func TestChatCompletionStreaming_FastPathUsageCarriesResolvedProviderName(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-123\",\"model\":\"gpt-4o-mini\",\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n"
	usageLog := &collectingUsageLogger{
		config: usage.Config{Enabled: true},
	}
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		providerNames: map[string]string{
			"gpt-4o-mini": "openai_test",
		},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(streamData)),
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(usageLog.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLog.entries))
	}
	if got := usageLog.entries[0].ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want openai_test", got)
	}
}

func TestChatCompletionStreaming_FastPathSkipsQualifiedModelRewrite(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-123\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\ndata: [DONE]\n\n"
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
			providerTypes: map[string]string{
				"gpt-4o-mini": "openai",
			},
			streamData: streamData,
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers: map[string][]string{
					"Content-Type": {"text/event-stream"},
				},
				Body: io.NopCloser(strings.NewReader("data: should-not-be-used\n\n")),
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"openai/gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if provider.lastPassthroughReq != nil {
		t.Fatal("lastPassthroughReq != nil, want rewritten request to use StreamChatCompletion path")
	}
	if provider.capturedChatReq == nil {
		t.Fatal("capturedChatReq = nil, want StreamChatCompletion request")
	}
	if provider.capturedChatReq.Model != "gpt-4o-mini" {
		t.Fatalf("captured model = %q, want gpt-4o-mini", provider.capturedChatReq.Model)
	}
	if provider.capturedChatReq.Provider != "openai" {
		t.Fatalf("captured provider = %q, want openai", provider.capturedChatReq.Provider)
	}
	if got := rec.Body.String(); got != streamData {
		t.Fatalf("stream body = %q, want %q", got, streamData)
	}
}

func TestChatCompletionStreaming_FastPathSkipsProviderFieldRewrite(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-123\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\ndata: [DONE]\n\n"
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
			providerTypes: map[string]string{
				"openai/gpt-4o-mini": "openai",
			},
			streamData: streamData,
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers: map[string][]string{
					"Content-Type": {"text/event-stream"},
				},
				Body: io.NopCloser(strings.NewReader("data: should-not-be-used\n\n")),
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"gpt-4o-mini","provider":"openai","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if provider.lastPassthroughReq != nil {
		t.Fatal("lastPassthroughReq != nil, want provider field rewrite to use StreamChatCompletion path")
	}
	if provider.capturedChatReq == nil {
		t.Fatal("capturedChatReq = nil, want StreamChatCompletion request")
	}
	if provider.capturedChatReq.Provider != "openai" {
		t.Fatalf("captured provider = %q, want openai", provider.capturedChatReq.Provider)
	}
	if got := rec.Body.String(); got != streamData {
		t.Fatalf("stream body = %q, want %q", got, streamData)
	}
}

func TestHandleStreamingResponse_FlushesEachChunk(t *testing.T) {
	e := echo.New()
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := &flushCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
	c := e.NewContext(req, rec)

	stream := &chunkedReadCloser{
		chunks: [][]byte{
			[]byte("data: {\"id\":\"1\"}\n\n"),
			[]byte("data: {\"id\":\"2\"}\n\n"),
			[]byte("data: [DONE]\n\n"),
		},
	}

	err := handler.translatedInference().handleStreamingResponse(c, nil, "gpt-4o-mini", "openai", "primary-openai", func() (io.ReadCloser, error) {
		return stream, nil
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if rec.flushes != 4 {
		t.Fatalf("expected 4 flushes (headers + 3 chunks), got %d", rec.flushes)
	}

	if got := rec.Body.String(); got != "data: {\"id\":\"1\"}\n\ndata: {\"id\":\"2\"}\n\ndata: [DONE]\n\n" {
		t.Fatalf("unexpected body %q", got)
	}
}

func TestFlushStream_ReturnsReadError(t *testing.T) {
	expectedErr := errors.New("stream read failed")
	stream := &erroringReadCloser{
		data: []byte("data: {\"id\":\"1\"}\n\n"),
		err:  expectedErr,
	}

	err := flushStream(io.Discard, stream)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected read error %v, got %v", expectedErr, err)
	}
}

func TestFlushStream_ReturnsWriteError(t *testing.T) {
	expectedErr := errors.New("client write failed")
	stream := io.NopCloser(strings.NewReader("data: {\"id\":\"1\"}\n\n"))

	err := flushStream(&erroringWriter{err: expectedErr}, stream)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected write error %v, got %v", expectedErr, err)
	}
}

func TestRequestIDFromContextOrHeader(t *testing.T) {
	t.Run("prefers context request id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Request-ID", "header-id")
		req = req.WithContext(core.WithRequestID(req.Context(), "context-id"))

		if got := requestIDFromContextOrHeader(req); got != "context-id" {
			t.Fatalf("requestIDFromContextOrHeader() = %q, want context-id", got)
		}
	})

	t.Run("falls back to header request id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Request-ID", "  header-id  ")

		if got := requestIDFromContextOrHeader(req); got != "header-id" {
			t.Fatalf("requestIDFromContextOrHeader() = %q, want header-id", got)
		}
	})

	t.Run("nil request returns empty", func(t *testing.T) {
		if got := requestIDFromContextOrHeader(nil); got != "" {
			t.Fatalf("requestIDFromContextOrHeader(nil) = %q, want empty", got)
		}
	})
}

func TestHandleStreamingResponse_RecordsStreamingError(t *testing.T) {
	expectedErr := errors.New("upstream stream failed")
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}

	e := echo.New()
	handler := NewHandler(&mockProvider{}, logger, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", "req-stream-1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{
		ID:        "entry-1",
		Timestamp: time.Now(),
		RequestID: "req-stream-1",
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		Data:      &auditlog.LogData{},
	})

	err := handler.translatedInference().handleStreamingResponse(c, nil, "gpt-4o-mini", "openai", "primary-openai", func() (io.ReadCloser, error) {
		return &erroringReadCloser{
			data: []byte("data: {\"id\":\"1\"}\n\n"),
			err:  expectedErr,
		}, nil
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if len(logger.entries) != 1 {
		t.Fatalf("expected 1 audit log entry, got %d", len(logger.entries))
	}

	entry := logger.entries[0]
	if entry.ErrorType != "stream_error" {
		t.Fatalf("expected stream_error, got %q", entry.ErrorType)
	}
	if entry.Data == nil || entry.Data.ErrorMessage != expectedErr.Error() {
		t.Fatalf("expected error message %q, got %+v", expectedErr.Error(), entry.Data)
	}
}

func TestChatCompletionStreaming_FlushesBeforeNextChunkArrives(t *testing.T) {
	secondChunkStarted := make(chan struct{})
	releaseSecondChunk := make(chan struct{})

	provider := &streamingProviderWithCustomReader{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
		},
		reader: &delayedChunkReadCloser{
			chunks: []delayedChunk{
				{data: []byte("data: {\"id\":\"1\"}\n\n")},
				{
					data:    []byte("data: [DONE]\n\n"),
					started: secondChunkStarted,
					release: releaseSecondChunk,
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/v1/chat/completions", handler.ChatCompletion)

	srv := httptest.NewServer(e)
	defer srv.Close()

	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	readResult := make(chan struct {
		n   int
		err error
		buf []byte
	}, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := resp.Body.Read(buf)
		readResult <- struct {
			n   int
			err error
			buf []byte
		}{n: n, err: err, buf: buf}
	}()

	select {
	case <-secondChunkStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server to start reading the delayed second chunk")
	}

	var result struct {
		n   int
		err error
		buf []byte
	}
	select {
	case result = <-readResult:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk to reach the client before releasing the second chunk")
	}

	close(releaseSecondChunk)

	if result.err != nil {
		t.Fatalf("read first chunk: %v", result.err)
	}

	firstChunk := string(result.buf[:result.n])
	if !strings.Contains(firstChunk, `"id":"1"`) {
		t.Fatalf("expected first streamed chunk before delayed tail, got %q", firstChunk)
	}
}

func TestHealth(t *testing.T) {
	e := echo.New()
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Health(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("expected ok status in body")
	}
}

func TestListModels(t *testing.T) {
	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:      "gpt-4o-mini",
					Object:  "model",
					Created: 1721172741,
					OwnedBy: "system",
				},
				{
					ID:      "gpt-4-turbo",
					Object:  "model",
					Created: 1712361441,
					OwnedBy: "system",
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ListModels(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"object":"list"`) {
		t.Errorf("response missing object field, got: %s", body)
	}
	if !strings.Contains(body, "gpt-4o-mini") {
		t.Errorf("response missing gpt-4o-mini model, got: %s", body)
	}
	if !strings.Contains(body, "gpt-4-turbo") {
		t.Errorf("response missing gpt-4-turbo model, got: %s", body)
	}
}

func TestListModels_MergesExposedModelsWithoutAliasProviderDecorator(t *testing.T) {
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		models: map[string]core.Model{
			"openai/gpt-4o": {ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
		},
	}
	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true},
	), catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:      "gpt-4o",
					Object:  "model",
					Created: 1721172741,
					OwnedBy: "openai",
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.exposedModelLister = service

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.ListModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `"id":"gpt-4o"`)
	require.Contains(t, body, `"id":"smart"`)
}

func TestListModels_KeepOnlyAliasesOmitsProviderModels(t *testing.T) {
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		models: map[string]core.Model{
			"openai/gpt-4o": {ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
		},
	}
	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true},
	), catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.exposedModelLister = service
	handler.keepOnlyAliasesAtModelsEndpoint = true

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.ListModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp core.ModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.Equal(t, "smart", resp.Data[0].ID)
}

func TestListModels_FiltersExposedModelsWhenAuthorizerIsPresent(t *testing.T) {
	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	authorizer := &recordingModelAuthorizer{
		allow: func(selector core.ModelSelector) bool {
			return selector.QualifiedModel() != "openai/gpt-5"
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.modelAuthorizer = authorizer
	handler.exposedModelLister = staticExposedModelLister{
		models: []core.Model{
			{ID: "openai/gpt-5", Object: "model", OwnedBy: "openai"},
			{ID: "openai/gpt-4o-mini", Object: "model", OwnedBy: "openai"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ListModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `"id":"gpt-4o"`)
	require.Contains(t, body, `"id":"openai/gpt-4o-mini"`)
	require.NotContains(t, body, `"id":"openai/gpt-5"`)
}

func TestListModelsError(t *testing.T) {
	mock := &mockProvider{
		err: io.EOF, // Simulate an error
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ListModels(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "error") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

// Tests for typed error handling

func TestHandleError_ProviderError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewProviderError("openai", http.StatusBadGateway, "upstream error", nil),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "provider_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "upstream error") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

func TestHandleError_RateLimitError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewRateLimitError("openai", "rate limit exceeded"),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "rate_limit_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "rate limit exceeded") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

func TestHandleError_InvalidRequestError(t *testing.T) {
	param := "model"
	code := "model_not_found"
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewInvalidRequestError("invalid parameters", nil).WithParam(param).WithCode(code),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	errorBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error body = %#v, want object", body["error"])
	}

	if errorBody["type"] != "invalid_request_error" {
		t.Errorf("error.type = %v, want invalid_request_error", errorBody["type"])
	}
	if errorBody["message"] != "invalid parameters" {
		t.Errorf("error.message = %v, want invalid parameters", errorBody["message"])
	}
	if errorBody["param"] != param {
		t.Errorf("error.param = %v, want %v", errorBody["param"], param)
	}
	if errorBody["code"] != code {
		t.Errorf("error.code = %v, want %v", errorBody["code"], code)
	}
}

func TestHandleError_AuthenticationError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewAuthenticationError("openai", "invalid API key"),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "authentication_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "invalid API key") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

func TestHandleError_NotFoundError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewNotFoundError("model not found"),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "not_found_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "model not found") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

func TestHandleError_StreamingError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewRateLimitError("openai", "rate limit exceeded during streaming"),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "stream": true, "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "rate_limit_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
}

func TestHandleError_UnexpectedErrorUsesOpenAISchema(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             errors.New("boom"),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	errorBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error body = %#v, want object", body["error"])
	}

	if errorBody["type"] != "provider_error" {
		t.Errorf("error.type = %v, want provider_error", errorBody["type"])
	}
	if errorBody["message"] != "an unexpected error occurred" {
		t.Errorf("error.message = %v, want unexpected error message", errorBody["message"])
	}
	if value, ok := errorBody["param"]; !ok || value != nil {
		t.Errorf("error.param = %v, want nil", value)
	}
	if value, ok := errorBody["code"]; !ok || value != nil {
		t.Errorf("error.code = %v, want nil", value)
	}
}

func TestChatCompletion_InvalidJSON(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{invalid json}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "invalid_request_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "invalid request body") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

func TestChatCompletion_InvalidContentType(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":123}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedChatReq != nil {
		t.Fatal("provider should not have been called for invalid content")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "invalid request body") {
		t.Fatalf("response should contain invalid request message, got: %s", body)
	}
	if !strings.Contains(body, "string or array of content parts") {
		t.Fatalf("response should mention supported content types, got: %s", body)
	}
}

func TestEmbeddings(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingResponse: &core.EmbeddingResponse{
			Object: "list",
			Data: []core.EmbeddingData{
				{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2,0.3]`), Index: 0},
			},
			Model:    "text-embedding-3-small",
			Provider: "openai",
			Usage:    core.EmbeddingUsage{PromptTokens: 5, TotalTokens: 5},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "text-embedding-3-small", "input": "hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Embeddings(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "text-embedding-3-small") {
		t.Errorf("response missing model, got: %s", body)
	}
	if !strings.Contains(body, "embedding") {
		t.Errorf("response missing embedding data, got: %s", body)
	}
}

func TestEmbeddings_InvalidJSON(t *testing.T) {
	mock := &mockProvider{supportedModels: []string{"text-embedding-3-small"}}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{bad json}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Embeddings(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestEmbeddings_ProviderReturnsError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingErr:    core.NewInvalidRequestError("embeddings not supported by this provider", nil),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "text-embedding-3-small", "input": "hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Embeddings(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "embeddings not supported") {
		t.Errorf("expected error message about embeddings, got: %s", body)
	}
}

func TestEmbeddings_WithUsageTracking(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingResponse: &core.EmbeddingResponse{
			Object: "list",
			Data: []core.EmbeddingData{
				{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2,0.3]`), Index: 0},
			},
			Model: "text-embedding-3-small",
			Usage: core.EmbeddingUsage{PromptTokens: 10, TotalTokens: 10},
		},
	}

	var capturedEntry *usage.UsageEntry
	usageLog := &capturingUsageLogger{
		config:   usage.Config{Enabled: true},
		captured: &capturedEntry,
	}

	inputPrice := 0.02
	resolver := &mockPricingResolver{
		pricing: &core.ModelPricing{
			Currency:     "USD",
			InputPerMtok: &inputPrice,
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, usageLog, resolver)

	reqBody := `{"model": "text-embedding-3-small", "input": "hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-embed-usage")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Embeddings(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if capturedEntry == nil {
		t.Fatal("expected usage entry to be captured, got nil")
	}
	if capturedEntry.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", capturedEntry.InputTokens)
	}
	if capturedEntry.RequestID != "test-req-embed-usage" {
		t.Errorf("RequestID = %q, want %q", capturedEntry.RequestID, "test-req-embed-usage")
	}
	if capturedEntry.InputCost == nil || *capturedEntry.InputCost == 0 {
		t.Error("expected non-zero InputCost from pricing resolver")
	}
}

func TestListModels_TypedError(t *testing.T) {
	mock := &mockProvider{
		err: core.NewProviderError("openai", http.StatusBadGateway, "failed to list models", nil),
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ListModels(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "provider_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "failed to list models") {
		t.Errorf("response should contain error message, got: %s", body)
	}
}

func TestBatches(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		batchCreateResponse: &core.BatchResponse{
			ID:               "provider-batch-123",
			Object:           "batch",
			Status:           "in_progress",
			Endpoint:         "/v1/chat/completions",
			CompletionWindow: "24h",
			CreatedAt:        1234567890,
			RequestCounts: core.BatchRequestCounts{
				Total: 2,
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{
	  "completion_window":"24h",
	  "requests":[
	    {
	      "custom_id":"chat-1",
	      "method":"POST",
	      "url":"/v1/chat/completions",
	      "body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}
	    }
	  ]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp core.BatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Object != "batch" {
		t.Errorf("Object = %q, want %q", resp.Object, "batch")
	}
	if resp.Status != "in_progress" {
		t.Errorf("Status = %q, want %q", resp.Status, "in_progress")
	}
	if resp.Provider != "mock" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "mock")
	}
	if resp.ProviderBatchID != "provider-batch-123" {
		t.Errorf("ProviderBatchID = %q, want %q", resp.ProviderBatchID, "provider-batch-123")
	}
}

func TestBatches_FullURLResponsesItemUsesSharedSelectorExtraction(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"openai/gpt-4o-mini": "openai",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:            "provider-batch-234",
			Object:        "batch",
			Status:        "in_progress",
			Endpoint:      "/v1/responses",
			CreatedAt:     1234567890,
			RequestCounts: core.BatchRequestCounts{Total: 1},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{
	  "completion_window":"24h",
	  "requests":[
	    {
	      "custom_id":"responses-1",
	      "method":"POST",
	      "url":"https://provider.example/v1/responses/",
	      "body":{"model":"gpt-4o-mini","provider":"openai","input":"Hi"}
	    }
	  ]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if mock.capturedBatchReq == nil {
		t.Fatal("capturedBatchReq = nil")
	}

	var resp core.BatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", resp.Provider)
	}
}

func TestBatches_UsesExplicitAliasResolverAndBatchPreparerWithoutAliasProviderDecorator(t *testing.T) {
	store := newAliasesTestStore(aliases.Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true})
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		models: map[string]core.Model{
			"openai/gpt-4o": {ID: "gpt-4o", Object: "model"},
		},
	}
	service, err := aliases.NewService(store, catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	mock := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:            "provider-batch-alias-inline-123",
			Object:        "batch",
			Status:        "in_progress",
			Endpoint:      "/v1/chat/completions",
			CreatedAt:     1234567890,
			RequestCounts: core.BatchRequestCounts{Total: 1},
		},
	}
	aliasBatchPreparer := aliases.NewBatchPreparer(mock, service)

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.modelResolver = service
	handler.batchRequestPreparer = aliasBatchPreparer

	reqBody := `{
	  "completion_window":"24h",
	  "requests":[
	    {
	      "custom_id":"chat-1",
	      "method":"POST",
	      "url":"/v1/chat/completions",
	      "body":{"model":"smart","messages":[{"role":"user","content":"Hi"}]}
	    }
	  ]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "openai", mock.capturedBatchProvider)
	require.NotNil(t, mock.capturedBatchReq)
	require.Len(t, mock.capturedBatchReq.Requests, 1)

	var rewritten map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(mock.capturedBatchReq.Requests[0].Body, &rewritten))
	require.Equal(t, `"gpt-4o"`, string(rewritten["model"]))
	_, hasProvider := rewritten["provider"]
	require.False(t, hasProvider)

	var resp core.BatchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "openai", resp.Provider)
}

func TestBatches_UsesExplicitBatchRequestPreparer(t *testing.T) {
	mock := &mockProvider{
		batchCreateResponse: &core.BatchResponse{
			ID:            "provider-batch-prepare-123",
			Object:        "batch",
			Status:        "in_progress",
			InputFileID:   "file_rewritten",
			CreatedAt:     1234567890,
			RequestCounts: core.BatchRequestCounts{Total: 1},
		},
		batchCreateHints: map[string]string{
			"provider-1": "/v1/chat/completions",
		},
	}
	preparer := &batchRequestPreparerStub{
		result: &core.BatchRewriteResult{
			Request: &core.BatchRequest{
				InputFileID:      "file_rewritten",
				Endpoint:         "/v1/chat/completions",
				CompletionWindow: "24h",
				Metadata:         map[string]string{"provider": "openai"},
			},
			RequestEndpointHints: map[string]string{
				"prepared-1": "/v1/responses",
			},
			OriginalInputFileID:  "file_source",
			RewrittenInputFileID: "file_rewritten",
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.batchRequestPreparer = preparer
	store := batchstore.NewMemoryStore()
	handler.SetBatchStore(store)

	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "openai", preparer.capturedProvider)
	require.NotNil(t, preparer.capturedReq)
	require.Equal(t, "file_source", preparer.capturedReq.InputFileID)
	require.Equal(t, "openai", mock.capturedBatchProvider)
	require.NotNil(t, mock.capturedBatchReq)
	require.Equal(t, "file_rewritten", mock.capturedBatchReq.InputFileID)
	require.Equal(t, "openai", mock.clearedBatchHintProvider)
	require.Equal(t, "provider-batch-prepare-123", mock.clearedBatchHintID)

	var created core.BatchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Equal(t, "file_source", created.InputFileID)

	stored, err := store.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "file_source", stored.Batch.InputFileID)
	require.Equal(t, "file_source", stored.OriginalInputFileID)
	require.Equal(t, "file_rewritten", stored.RewrittenInputFileID)
	require.Equal(t, map[string]string{
		"prepared-1": "/v1/responses",
		"provider-1": "/v1/chat/completions",
	}, stored.RequestEndpointByCustomID)
}

func TestBatches_CleansUpPreparedInputFileOnCreateFailure(t *testing.T) {
	mock := &mockProvider{
		batchErr: errors.New("provider boom"),
	}
	preparer := &batchRequestPreparerStub{
		result: &core.BatchRewriteResult{
			Request: &core.BatchRequest{
				InputFileID: "file_rewritten",
				Endpoint:    "/v1/chat/completions",
				Metadata:    map[string]string{"provider": "openai"},
			},
			OriginalInputFileID:  "file_source",
			RewrittenInputFileID: "file_rewritten",
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.batchRequestPreparer = preparer

	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "metadata":{"provider":"openai"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"file_rewritten"}, mock.capturedFileDeleteIDs)
}

func TestBatches_MixedProviderRejected(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku-20240307"},
		providerTypes: map[string]string{
			"gpt-4o-mini":             "openai",
			"claude-3-haiku-20240307": "anthropic",
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{
	  "requests":[
	    {
	      "custom_id":"one",
	      "url":"/v1/chat/completions",
	      "body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}
	    },
	    {
	      "custom_id":"two",
	      "url":"/v1/chat/completions",
	      "body":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"Hi"}]}
	    }
	  ]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestBatches_InputFileRewritesAliasesAndPersistsBatchPreparation(t *testing.T) {
	store := newAliasesTestStore(aliases.Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true})
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		models: map[string]core.Model{
			"openai/gpt-4o": {ID: "gpt-4o", Object: "model"},
		},
	}
	service, err := aliases.NewService(store, catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	inner := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		fileContentResponse: &core.FileContentResponse{
			ID:       "file_source",
			Filename: "batch.jsonl",
			Data:     []byte("{\"custom_id\":\"chat-1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}]}}\n"),
		},
		fileCreateResponse: &core.FileObject{
			ID:       "file_rewritten",
			Object:   "file",
			Filename: "batch.jsonl",
			Purpose:  "batch",
			Provider: "openai",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:          "provider-batch-1",
			Object:      "batch",
			Status:      "validating",
			Endpoint:    "/v1/chat/completions",
			CreatedAt:   1234567890,
			InputFileID: "file_rewritten",
			RequestCounts: core.BatchRequestCounts{
				Total: 1,
			},
		},
	}

	provider := aliases.NewProvider(inner, service)
	handler := NewHandler(provider, nil, nil, nil)
	batchStore := batchstore.NewMemoryStore()
	handler.SetBatchStore(batchStore)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, inner.capturedBatchReq)
	require.Equal(t, "file_rewritten", inner.capturedBatchReq.InputFileID)
	require.Len(t, inner.capturedFileCreateReqs, 1)
	require.Contains(t, string(inner.capturedFileCreateReqs[0].Content), `"model":"gpt-4o"`)

	var created core.BatchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Equal(t, "file_source", created.InputFileID)

	stored, err := batchStore.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "file_source", stored.Batch.InputFileID)
	require.Equal(t, "file_source", stored.OriginalInputFileID)
	require.Equal(t, "file_rewritten", stored.RewrittenInputFileID)
}

func TestBatches_InputFileRejectsUnsupportedExplicitProviderSelector(t *testing.T) {
	store := newAliasesTestStore(aliases.Alias{Name: "smart", TargetModel: "claude-3-7-sonnet", TargetProvider: "anthropic", Enabled: true})
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"anthropic/claude-3-7-sonnet": true,
		},
		providerTypes: map[string]string{
			"anthropic/claude-3-7-sonnet": "anthropic",
		},
		models: map[string]core.Model{
			"anthropic/claude-3-7-sonnet": {ID: "claude-3-7-sonnet", Object: "model"},
		},
	}
	service, err := aliases.NewService(store, catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	mock := &mockProvider{
		supportedModels: []string{"claude-3-7-sonnet"},
		providerTypes: map[string]string{
			"anthropic/claude-3-7-sonnet": "anthropic",
		},
		fileContentResponse: &core.FileContentResponse{
			ID:       "file_source",
			Filename: "batch.jsonl",
			Data:     []byte("{\"custom_id\":\"chat-1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"provider\":\"openai\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}]}}\n"),
		},
	}
	aliasBatchPreparer := aliases.NewBatchPreparer(mock, service)

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)
	handler.modelResolver = service
	handler.batchRequestPreparer = aliasBatchPreparer

	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported model: openai/smart")
	require.Nil(t, mock.capturedBatchReq)
	require.Empty(t, mock.capturedFileCreateReqs)
}

func TestBatches_RollsBackPreparedInputAndUpstreamBatchWhenStoreCreateFails(t *testing.T) {
	inner := &mockProvider{
		batchCreateResponse: &core.BatchResponse{
			ID:              "provider-batch-1",
			ProviderBatchID: "provider-batch-1",
			Object:          "batch",
			Status:          "validating",
			Endpoint:        "/v1/chat/completions",
		},
		batchCreateHints: map[string]string{"req-1": "/v1/responses"},
	}

	handler := NewHandler(inner, nil, nil, nil)
	handler.batchRequestPreparer = &batchRequestPreparerStub{
		result: &core.BatchRewriteResult{
			Request: &core.BatchRequest{
				InputFileID:      "file_rewritten",
				Endpoint:         "/v1/chat/completions",
				CompletionWindow: "24h",
				Metadata:         map[string]string{"provider": "openai"},
			},
			OriginalInputFileID:  "file_source",
			RewrittenInputFileID: "file_rewritten",
			RequestEndpointHints: map[string]string{"req-1": "/v1/responses"},
		},
	}
	handler.SetBatchStore(&failingBatchStore{createErr: errors.New("boom")})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, []string{"file_rewritten"}, inner.capturedFileDeleteIDs)
	require.Equal(t, "openai", inner.capturedBatchCancelProvider)
	require.Equal(t, "provider-batch-1", inner.capturedBatchCancelID)
	require.Equal(t, "openai", inner.clearedBatchHintProvider)
	require.Equal(t, "provider-batch-1", inner.clearedBatchHintID)
}

func TestBatches_InputFileRejectsDisabledAlias(t *testing.T) {
	store := newAliasesTestStore(aliases.Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: false})
	catalog := &aliasesTestCatalog{
		supported: map[string]bool{
			"openai/gpt-4o": true,
		},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		models: map[string]core.Model{
			"openai/gpt-4o": {ID: "gpt-4o", Object: "model"},
		},
	}
	service, err := aliases.NewService(store, catalog)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	inner := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes: map[string]string{
			"openai/gpt-4o": "openai",
		},
		fileContentResponse: &core.FileContentResponse{
			ID:       "file_source",
			Filename: "batch.jsonl",
			Data:     []byte("{\"custom_id\":\"chat-1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}]}}\n"),
		},
	}

	provider := aliases.NewProvider(inner, service)
	handler := NewHandler(provider, nil, nil, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported model: smart")
	require.Nil(t, inner.capturedBatchReq)
	require.Empty(t, inner.capturedFileCreateReqs)
}

func TestGetBatch_PreservesClientInputFileIDAndCleansUpRewrittenFile(t *testing.T) {
	provider := &mockProvider{
		batchGetResponse: &core.BatchResponse{
			ID:              "provider-batch-1",
			Object:          "batch",
			Status:          "completed",
			InputFileID:     "file_hidden",
			ProviderBatchID: "provider-batch-1",
		},
	}

	handler := NewHandler(provider, nil, nil, nil)
	store := batchstore.NewMemoryStore()
	handler.SetBatchStore(store)
	require.NoError(t, store.Create(context.Background(), &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:              "batch_1",
			Object:          "batch",
			Provider:        "openai",
			ProviderBatchID: "provider-batch-1",
			Status:          "in_progress",
			InputFileID:     "file_source",
		},
		OriginalInputFileID:  "file_source",
		RewrittenInputFileID: "file_hidden",
	}))

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/batches/batch_1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/batches/:id")
	setPathParam(c, "id", "batch_1")

	err := handler.GetBatch(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"file_hidden"}, provider.capturedFileDeleteIDs)

	var resp core.BatchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "file_source", resp.InputFileID)
	require.Equal(t, "completed", resp.Status)

	stored, err := store.Get(context.Background(), "batch_1")
	require.NoError(t, err)
	require.Equal(t, "", stored.RewrittenInputFileID)
	require.Equal(t, "file_source", stored.Batch.InputFileID)
}

func TestBatches_EmptyRequests(t *testing.T) {
	e := echo.New()
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	reqBody := `{"requests":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Batches(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestBatches_LifecycleEndpoints(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		batchCreateResponse: &core.BatchResponse{
			ID:               "provider-batch-1",
			Object:           "batch",
			Status:           "in_progress",
			CreatedAt:        1000,
			RequestCounts:    core.BatchRequestCounts{Total: 1},
			CompletionWindow: "24h",
		},
		batchGetResponse: &core.BatchResponse{
			ID:               "provider-batch-1",
			Object:           "batch",
			Status:           "completed",
			CreatedAt:        1000,
			RequestCounts:    core.BatchRequestCounts{Total: 1, Completed: 1},
			CompletionWindow: "24h",
		},
		batchCancelResponse: &core.BatchResponse{
			ID:               "provider-batch-1",
			Object:           "batch",
			Status:           "cancelled",
			CreatedAt:        1000,
			RequestCounts:    core.BatchRequestCounts{Total: 1, Completed: 1},
			CompletionWindow: "24h",
		},
		batchResults: &core.BatchResultsResponse{
			Object:  "list",
			BatchID: "provider-batch-1",
			Data: []core.BatchResultItem{
				{Index: 0, StatusCode: 200, CustomID: "life-1"},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	// 1) Create
	createBody := `{
	  "endpoint":"/v1/chat/completions",
	  "requests":[{"custom_id":"life-1","method":"POST","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}}]
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	if err := handler.Batches(createCtx); err != nil {
		t.Fatalf("create handler returned error: %v", err)
	}
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200", createRec.Code)
	}

	var created core.BatchResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected created batch id")
	}

	// 2) Get
	getReq := httptest.NewRequest(http.MethodGet, "/v1/batches/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	getCtx := e.NewContext(getReq, getRec)
	getCtx.SetPath("/v1/batches/:id")
	setPathParam(getCtx, "id", created.ID)
	if err := handler.GetBatch(getCtx); err != nil {
		t.Fatalf("get handler returned error: %v", err)
	}
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", getRec.Code)
	}

	// 3) List
	listReq := httptest.NewRequest(http.MethodGet, "/v1/batches?limit=10", nil)
	listRec := httptest.NewRecorder()
	listCtx := e.NewContext(listReq, listRec)
	if err := handler.ListBatches(listCtx); err != nil {
		t.Fatalf("list handler returned error: %v", err)
	}
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listRec.Code)
	}
	var listResp core.BatchListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Data) == 0 {
		t.Fatal("expected at least one batch in list")
	}

	// 4) Results
	resReq := httptest.NewRequest(http.MethodGet, "/v1/batches/"+created.ID+"/results", nil)
	resRec := httptest.NewRecorder()
	resCtx := e.NewContext(resReq, resRec)
	resCtx.SetPath("/v1/batches/:id/results")
	setPathParam(resCtx, "id", created.ID)
	if err := handler.BatchResults(resCtx); err != nil {
		t.Fatalf("results handler returned error: %v", err)
	}
	if resRec.Code != http.StatusOK {
		t.Fatalf("results status = %d, want 200", resRec.Code)
	}
	var resultsResp core.BatchResultsResponse
	if err := json.Unmarshal(resRec.Body.Bytes(), &resultsResp); err != nil {
		t.Fatalf("decode results response: %v", err)
	}
	if resultsResp.BatchID != created.ID {
		t.Fatalf("results batch id = %q, want %q", resultsResp.BatchID, created.ID)
	}
	if len(resultsResp.Data) != 1 {
		t.Fatalf("results len = %d, want 1", len(resultsResp.Data))
	}

	// 5) Cancel (completed batch stays completed)
	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/batches/"+created.ID+"/cancel", nil)
	cancelRec := httptest.NewRecorder()
	cancelCtx := e.NewContext(cancelReq, cancelRec)
	cancelCtx.SetPath("/v1/batches/:id/cancel")
	setPathParam(cancelCtx, "id", created.ID)
	if err := handler.CancelBatch(cancelCtx); err != nil {
		t.Fatalf("cancel handler returned error: %v", err)
	}
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", cancelRec.Code)
	}
}

func TestBatchLifecyclePersistsAndUsesInternalEndpointHints(t *testing.T) {
	type ctxKey string

	mock := &mockProvider{
		supportedModels: []string{"claude-sonnet-4-5-20250929"},
		providerTypes: map[string]string{
			"claude-sonnet-4-5-20250929": "anthropic",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:               "provider-batch-1",
			Object:           "batch",
			Status:           "in_progress",
			CreatedAt:        1000,
			RequestCounts:    core.BatchRequestCounts{Total: 1},
			CompletionWindow: "24h",
		},
		batchCreateHints: map[string]string{
			"resp-1": "/v1/responses",
		},
		batchResultsHinted: &core.BatchResultsResponse{
			Object:  "list",
			BatchID: "provider-batch-1",
			Data: []core.BatchResultItem{
				{Index: 0, CustomID: "resp-1", URL: "/v1/responses", StatusCode: http.StatusOK},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
	  "endpoint":"/v1/responses",
	  "requests":[{"custom_id":"resp-1","method":"POST","body":{"model":"claude-sonnet-4-5-20250929","input":"hi"}}]
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(createBody))
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), ctxKey("phase"), "create"))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	if err := handler.Batches(createCtx); err != nil {
		t.Fatalf("create handler returned error: %v", err)
	}
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200", createRec.Code)
	}
	if strings.Contains(createRec.Body.String(), "request_endpoint_by_custom_id") {
		t.Fatalf("create response leaked internal hints: %s", createRec.Body.String())
	}
	if mock.capturedBatchProvider != "anthropic" {
		t.Fatalf("capturedBatchProvider = %q, want anthropic", mock.capturedBatchProvider)
	}
	if got := core.GetRequestID(mock.capturedBatchCtx); got == "" {
		t.Fatal("expected request ID on create batch provider context")
	}
	if got := mock.capturedBatchCtx.Value(ctxKey("phase")); got != "create" {
		t.Fatalf("capturedBatchCtx phase = %#v, want create", got)
	}
	if mock.clearedBatchHintProvider != "anthropic" {
		t.Fatalf("clearedBatchHintProvider = %q, want anthropic", mock.clearedBatchHintProvider)
	}
	if mock.clearedBatchHintID != "provider-batch-1" {
		t.Fatalf("clearedBatchHintID = %q, want provider-batch-1", mock.clearedBatchHintID)
	}

	var created core.BatchResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	resReq := httptest.NewRequest(http.MethodGet, "/v1/batches/"+created.ID+"/results", nil)
	resReq = resReq.WithContext(context.WithValue(resReq.Context(), ctxKey("phase"), "results"))
	resRec := httptest.NewRecorder()
	resCtx := e.NewContext(resReq, resRec)
	resCtx.SetPath("/v1/batches/:id/results")
	setPathParam(resCtx, "id", created.ID)
	if err := handler.BatchResults(resCtx); err != nil {
		t.Fatalf("results handler returned error: %v", err)
	}
	if resRec.Code != http.StatusOK {
		t.Fatalf("results status = %d, want 200", resRec.Code)
	}
	if got := mock.capturedBatchHints["resp-1"]; got != "/v1/responses" {
		t.Fatalf("capturedBatchHints[resp-1] = %q, want /v1/responses", got)
	}
	if mock.capturedBatchHintsProvider != "anthropic" {
		t.Fatalf("capturedBatchHintsProvider = %q, want anthropic", mock.capturedBatchHintsProvider)
	}
	if mock.capturedBatchHintsBatchID != "provider-batch-1" {
		t.Fatalf("capturedBatchHintsBatchID = %q, want provider-batch-1", mock.capturedBatchHintsBatchID)
	}
	if got := core.GetRequestID(mock.capturedBatchHintsCtx); got == "" {
		t.Fatal("expected request ID on batch results provider context")
	}
	if got := mock.capturedBatchHintsCtx.Value(ctxKey("phase")); got != "results" {
		t.Fatalf("capturedBatchHintsCtx phase = %#v, want results", got)
	}
}

func TestBatchResults_PendingReturnsConflict(t *testing.T) {
	notReadyErr := core.NewNotFoundError("Message Batch msgbatch_123 has no available results.")
	notReadyErr.Provider = "anthropic"

	mock := &mockProvider{
		supportedModels: []string{"claude-3-haiku-20240307"},
		providerTypes: map[string]string{
			"claude-3-haiku-20240307": "anthropic",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:            "msgbatch_123",
			Object:        "batch",
			Status:        "in_progress",
			CreatedAt:     1000,
			RequestCounts: core.BatchRequestCounts{Total: 1},
		},
		batchGetResponse: &core.BatchResponse{
			ID:            "msgbatch_123",
			Object:        "batch",
			Status:        "in_progress",
			CreatedAt:     1000,
			RequestCounts: core.BatchRequestCounts{Total: 1},
		},
		batchResultsErr: notReadyErr,
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
	  "endpoint":"/v1/chat/completions",
	  "requests":[{"custom_id":"pending-1","method":"POST","body":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"hi"}]}}]
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	if err := handler.Batches(createCtx); err != nil {
		t.Fatalf("create handler returned error: %v", err)
	}

	var created core.BatchResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	resReq := httptest.NewRequest(http.MethodGet, "/v1/batches/"+created.ID+"/results", nil)
	resRec := httptest.NewRecorder()
	resCtx := e.NewContext(resReq, resRec)
	resCtx.SetPath("/v1/batches/:id/results")
	setPathParam(resCtx, "id", created.ID)
	if err := handler.BatchResults(resCtx); err != nil {
		t.Fatalf("results handler returned error: %v", err)
	}
	if resRec.Code != http.StatusConflict {
		t.Fatalf("results status = %d, want 409", resRec.Code)
	}
	if !strings.Contains(resRec.Body.String(), "results are not ready yet") {
		t.Fatalf("results body should describe pending state, got: %s", resRec.Body.String())
	}
}

func TestBatchResults_DoesNotCleanupRewrittenFileBeforeTerminalStatus(t *testing.T) {
	mock := &mockProvider{
		batchResults: &core.BatchResultsResponse{
			Object:  "list",
			BatchID: "provider-batch-1",
			Data: []core.BatchResultItem{
				{Index: 0, StatusCode: 200, CustomID: "partial-1"},
			},
		},
	}

	handler := NewHandler(mock, nil, nil, nil)
	store := batchstore.NewMemoryStore()
	handler.SetBatchStore(store)
	require.NoError(t, store.Create(context.Background(), &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:              "batch_1",
			Object:          "batch",
			Provider:        "openai",
			ProviderBatchID: "provider-batch-1",
			Status:          "in_progress",
			InputFileID:     "file_source",
		},
		OriginalInputFileID:  "file_source",
		RewrittenInputFileID: "file_hidden",
	}))

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/batches/batch_1/results", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/batches/:id/results")
	setPathParam(c, "id", "batch_1")

	err := handler.BatchResults(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, mock.capturedFileDeleteIDs)

	stored, err := store.Get(context.Background(), "batch_1")
	require.NoError(t, err)
	require.Equal(t, "file_hidden", stored.RewrittenInputFileID)
	require.Len(t, stored.Batch.Results, 1)
}

func TestBatchResults_LogsUsageOnce(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"claude-3-haiku-20240307"},
		providerTypes: map[string]string{
			"claude-3-haiku-20240307": "anthropic",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:            "msgbatch_usage_1",
			Object:        "batch",
			Status:        "completed",
			CreatedAt:     1000,
			RequestCounts: core.BatchRequestCounts{Total: 1, Completed: 1},
			Metadata:      map[string]string{"upstream": "true"},
		},
		batchResults: &core.BatchResultsResponse{
			Object:  "list",
			BatchID: "msgbatch_usage_1",
			Data: []core.BatchResultItem{
				{
					Index:      0,
					CustomID:   "usage-1",
					StatusCode: 200,
					Response: map[string]any{
						"id":    "msg_usage_1",
						"model": "claude-3-haiku-20240307",
						"usage": map[string]any{
							"input_tokens":            1000.0,
							"output_tokens":           500.0,
							"total_tokens":            1500.0,
							"cache_read_input_tokens": 120.0,
						},
					},
				},
			},
		},
	}

	inputPrice := 10.0
	outputPrice := 20.0
	batchInputPrice := 1.0
	batchOutputPrice := 2.0
	resolver := &mockPricingResolver{
		pricing: &core.ModelPricing{
			Currency:           "USD",
			InputPerMtok:       &inputPrice,
			OutputPerMtok:      &outputPrice,
			BatchInputPerMtok:  &batchInputPrice,
			BatchOutputPerMtok: &batchOutputPrice,
		},
	}

	usageLog := &collectingUsageLogger{
		config: usage.Config{Enabled: true},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, usageLog, resolver)

	createBody := `{
	  "endpoint":"/v1/chat/completions",
	  "requests":[{"custom_id":"usage-1","method":"POST","body":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"hi"}]}}]
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Request-ID", "batch-usage-request-id")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	if err := handler.Batches(createCtx); err != nil {
		t.Fatalf("create handler returned error: %v", err)
	}
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200", createRec.Code)
	}

	var created core.BatchResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// First results call should log usage.
	resReq1 := httptest.NewRequest(http.MethodGet, "/v1/batches/"+created.ID+"/results", nil)
	resRec1 := httptest.NewRecorder()
	resCtx1 := e.NewContext(resReq1, resRec1)
	resCtx1.SetPath("/v1/batches/:id/results")
	setPathParam(resCtx1, "id", created.ID)
	if err := handler.BatchResults(resCtx1); err != nil {
		t.Fatalf("results handler returned error: %v", err)
	}
	if resRec1.Code != http.StatusOK {
		t.Fatalf("results status = %d, want 200", resRec1.Code)
	}

	// Second results call should not duplicate usage writes.
	resReq2 := httptest.NewRequest(http.MethodGet, "/v1/batches/"+created.ID+"/results", nil)
	resRec2 := httptest.NewRecorder()
	resCtx2 := e.NewContext(resReq2, resRec2)
	resCtx2.SetPath("/v1/batches/:id/results")
	setPathParam(resCtx2, "id", created.ID)
	if err := handler.BatchResults(resCtx2); err != nil {
		t.Fatalf("second results handler returned error: %v", err)
	}
	if resRec2.Code != http.StatusOK {
		t.Fatalf("second results status = %d, want 200", resRec2.Code)
	}

	if len(usageLog.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLog.entries))
	}

	entry := usageLog.entries[0]
	if entry.RequestID != "batch-usage-request-id" {
		t.Errorf("RequestID = %q, want %q", entry.RequestID, "batch-usage-request-id")
	}
	if entry.Endpoint != "/v1/batches" {
		t.Errorf("Endpoint = %q, want %q", entry.Endpoint, "/v1/batches")
	}
	if entry.ProviderID != "msg_usage_1" {
		t.Errorf("ProviderID = %q, want %q", entry.ProviderID, "msg_usage_1")
	}
	if entry.InputTokens != 1000 || entry.OutputTokens != 500 || entry.TotalTokens != 1500 {
		t.Errorf("unexpected token totals: input=%d output=%d total=%d", entry.InputTokens, entry.OutputTokens, entry.TotalTokens)
	}
	if entry.TotalCost == nil || *entry.TotalCost <= 0 {
		t.Fatalf("expected non-zero total cost, got %+v", entry.TotalCost)
	}
	// 1000 * 1$/Mt + 500 * 2$/Mt = 0.001 + 0.001 = 0.002
	expectedTotalCost := 0.002
	delta := *entry.TotalCost - expectedTotalCost
	if delta < 0 {
		delta = -delta
	}
	if delta > 1e-9 {
		t.Errorf("TotalCost = %.6f, want %.6f", *entry.TotalCost, expectedTotalCost)
	}
	if entry.RawData == nil {
		t.Fatal("expected raw usage data")
	}
	if entry.RawData["batch_custom_id"] != "usage-1" {
		t.Errorf("batch_custom_id = %v, want %q", entry.RawData["batch_custom_id"], "usage-1")
	}
}

func TestGetBatch_NotFound(t *testing.T) {
	e := echo.New()
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/batches/missing", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/batches/:id")
	setPathParam(c, "id", "missing")

	if err := handler.GetBatch(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestResponsesLifecycle_RetrievesStoredResponseAndInputItems(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:        "resp_store_1",
			Object:    "response",
			CreatedAt: 1000,
			Model:     "gpt-5-mini",
			Status:    "completed",
			Output: []core.ResponsesOutputItem{
				{
					ID:     "msg_1",
					Type:   "message",
					Role:   "assistant",
					Status: "completed",
					Content: []core.ResponsesContentItem{
						{Type: "output_text", Text: "hello"},
					},
				},
			},
		},
	}
	srv := New(provider, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", createRec.Code, createRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_store_1", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", getRec.Code, getRec.Body.String())
	}
	var got core.ResponsesResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.ID != "resp_store_1" || got.Provider != "mock" {
		t.Fatalf("stored response = %+v, want id resp_store_1 provider mock", got)
	}

	itemsReq := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_store_1/input_items?order=asc", nil)
	itemsRec := httptest.NewRecorder()
	srv.ServeHTTP(itemsRec, itemsReq)
	if itemsRec.Code != http.StatusOK {
		t.Fatalf("input items status = %d, want 200 (%s)", itemsRec.Code, itemsRec.Body.String())
	}
	var items core.ResponseInputItemListResponse
	if err := json.Unmarshal(itemsRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode input items response: %v", err)
	}
	if items.Object != "list" || len(items.Data) != 1 || items.HasMore {
		t.Fatalf("input items = %+v, want one-item list", items)
	}
	var first map[string]any
	if err := json.Unmarshal(items.Data[0], &first); err != nil {
		t.Fatalf("decode first input item: %v", err)
	}
	if first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("input item = %+v, want user message", first)
	}
	content, ok := first["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("input content = %+v, want one content item", first["content"])
	}
	text, ok := content[0].(map[string]any)
	if !ok || text["type"] != "input_text" || text["text"] != "hello" {
		t.Fatalf("input content item = %+v, want input_text hello", content[0])
	}
}

func TestResponsesLifecycle_StoresConcreteProviderName(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "openai",
		},
		providerNames: map[string]string{
			"gpt-5-mini": "openai_primary",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_provider_name_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
		},
	}
	srv := New(provider, &Config{ResponseStore: store})

	createReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", createRec.Code, createRec.Body.String())
	}

	stored, err := store.Get(context.Background(), "resp_provider_name_1")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.Provider != "openai" {
		t.Fatalf("stored provider = %q, want openai", stored.Provider)
	}
	if stored.ProviderName != "openai_primary" {
		t.Fatalf("stored provider name = %q, want openai_primary", stored.ProviderName)
	}
}

func TestResponsesLifecycle_ReturnsSuccessWhenSnapshotStoreFails(t *testing.T) {
	observability.ResetMetrics()
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_store_failure_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
		},
	}
	srv := New(provider, &Config{ResponseStore: &failingResponseStore{err: errors.New("write failed")}})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "resp_store_failure_1" {
		t.Fatalf("response id = %q, want resp_store_failure_1", resp.ID)
	}

	counter := observability.ResponseSnapshotStoreFailures.WithLabelValues("mock", "", "store")
	if got := testutil.ToFloat64(counter); got != 1 {
		t.Fatalf("snapshot store failures = %v, want 1", got)
	}
}

func TestHandlerSetResponseStoreUpdatesCachedTranslatedInferenceService(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)
	first := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	second := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())

	handler.SetResponseStore(first)
	service := handler.translatedInference()
	if service.currentResponseStore() != first {
		t.Fatal("translatedInferenceService did not capture first response store")
	}

	handler.SetResponseStore(second)
	if service.currentResponseStore() != second {
		t.Fatal("SetResponseStore did not update cached translatedInferenceService response store")
	}
}

func TestHandlerSetResponseStoreIsConcurrentSafe(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)
	_ = handler.translatedInference()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			handler.SetResponseStore(responsestore.NewMemoryStore(responsestore.WithUnboundedRetention()))
		}()
		go func() {
			defer wg.Done()
			_ = handler.translatedInference().currentResponseStore()
			_ = handler.nativeResponses()
		}()
	}
	wg.Wait()
}

func TestResponsesLifecycle_CancelUnsupportedProviderReturnsCompatibilityError(t *testing.T) {
	base := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_cancel_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "in_progress",
		},
	}
	srv := New(&providerWithoutResponseLifecycle{inner: base}, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", createRec.Code, createRec.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_cancel_1/cancel", nil)
	cancelRec := httptest.NewRecorder()
	srv.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusNotImplemented {
		t.Fatalf("cancel status = %d, want 501 (%s)", cancelRec.Code, cancelRec.Body.String())
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(cancelRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cancel error: %v", err)
	}
	if body["error"]["code"] != "unsupported_response_operation" {
		t.Fatalf("error code = %v, want unsupported_response_operation", body["error"]["code"])
	}
}

func TestResponsesLifecycle_DeleteStoredResponseWithoutNativeSupport(t *testing.T) {
	base := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_delete_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
		},
	}
	srv := New(&providerWithoutResponseLifecycle{inner: base}, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", createRec.Code, createRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/responses/resp_delete_1", nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", deleteRec.Code, deleteRec.Body.String())
	}
	var deleted core.ResponseDeleteResponse
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleted.ID != "resp_delete_1" || !deleted.Deleted {
		t.Fatalf("delete response = %+v, want deleted resp_delete_1", deleted)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_delete_1", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotImplemented {
		t.Fatalf("get after delete status = %d, want 501 (%s)", getRec.Code, getRec.Body.String())
	}
}

func TestResponsesUtilityRoutes(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responseInputTokensResponse: &core.ResponseInputTokensResponse{
			Object:      "response.input_tokens",
			InputTokens: 42,
		},
		responseCompactResponse: &core.ResponseCompactResponse{
			ID:     "cmp_42",
			Object: "response.compaction",
		},
	}
	srv := New(provider, nil)

	tokensReq := httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	tokensReq.Header.Set("Content-Type", "application/json")
	tokensRec := httptest.NewRecorder()
	srv.ServeHTTP(tokensRec, tokensReq)
	if tokensRec.Code != http.StatusOK {
		t.Fatalf("input_tokens status = %d, want 200 (%s)", tokensRec.Code, tokensRec.Body.String())
	}
	var tokens core.ResponseInputTokensResponse
	if err := json.Unmarshal(tokensRec.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode input_tokens response: %v", err)
	}
	if tokens.InputTokens != 42 {
		t.Fatalf("input tokens = %d, want 42", tokens.InputTokens)
	}

	compactReq := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	compactReq.Header.Set("Content-Type", "application/json")
	compactRec := httptest.NewRecorder()
	srv.ServeHTTP(compactRec, compactReq)
	if compactRec.Code != http.StatusOK {
		t.Fatalf("compact status = %d, want 200 (%s)", compactRec.Code, compactRec.Body.String())
	}
	var compact core.ResponseCompactResponse
	if err := json.Unmarshal(compactRec.Body.Bytes(), &compact); err != nil {
		t.Fatalf("decode compact response: %v", err)
	}
	if compact.ID != "cmp_42" || compact.Provider != "mock" {
		t.Fatalf("compact response = %+v, want id cmp_42 provider mock", compact)
	}
	if len(provider.capturedResponseUtilityReqs) != 2 {
		t.Fatalf("utility calls = %d, want 2", len(provider.capturedResponseUtilityReqs))
	}
	wantUtility := []responseUtilityCall{
		{provider: "mock", operation: "CountResponseInputTokens"},
		{provider: "mock", operation: "CompactResponse"},
	}
	if !reflect.DeepEqual(provider.capturedResponseUtility, wantUtility) {
		t.Fatalf("utility routing = %+v, want %+v", provider.capturedResponseUtility, wantUtility)
	}
}

func TestResponsesUtilityRoutesReturnProviderErrorWhenProviderTypeMissing(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "",
		},
	}
	srv := New(provider, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, core.ErrorTypeProvider, envelope.Error.Type)
	require.Equal(t, "unable to resolve provider for response utility operation", envelope.Error.Message)
	require.Empty(t, provider.capturedResponseUtilityReqs)
}

// mockUsageLogger implements usage.LoggerInterface for testing.
type mockUsageLogger struct {
	config usage.Config
}

func (m *mockUsageLogger) Write(_ *usage.UsageEntry) {}
func (m *mockUsageLogger) Config() usage.Config      { return m.config }
func (m *mockUsageLogger) Close() error              { return nil }

type capturingUsageLogger struct {
	config   usage.Config
	captured **usage.UsageEntry
}

func (c *capturingUsageLogger) Write(entry *usage.UsageEntry) { *c.captured = entry }
func (c *capturingUsageLogger) Config() usage.Config          { return c.config }
func (c *capturingUsageLogger) Close() error                  { return nil }

type collectingUsageLogger struct {
	config  usage.Config
	entries []*usage.UsageEntry
}

func (c *collectingUsageLogger) Write(entry *usage.UsageEntry) {
	if entry == nil {
		return
	}
	c.entries = append(c.entries, entry)
}

func (c *collectingUsageLogger) Config() usage.Config { return c.config }
func (c *collectingUsageLogger) Close() error         { return nil }

type mockPricingResolver struct {
	pricing *core.ModelPricing
}

func (m *mockPricingResolver) ResolvePricing(_, _ string) *core.ModelPricing {
	return m.pricing
}

// capturingProvider is a mockProvider that captures the request passed to StreamResponses/StreamChatCompletion.
type capturingProvider struct {
	mockProvider
	capturedChatReq      *core.ChatRequest
	capturedResponsesReq *core.ResponsesRequest
	capturedEmbeddingReq *core.EmbeddingRequest
}

func (c *capturingProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	c.capturedChatReq = req
	if c.err != nil {
		return nil, c.err
	}
	return c.response, nil
}

func (c *capturingProvider) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	c.capturedChatReq = req
	return io.NopCloser(strings.NewReader(c.streamData)), nil
}

func (c *capturingProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	c.capturedResponsesReq = req
	if c.err != nil {
		return nil, c.err
	}
	return c.responsesResponse, nil
}

func (c *capturingProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	c.capturedResponsesReq = req
	return io.NopCloser(strings.NewReader(c.streamData)), nil
}

type chatBackedResponsesProvider struct {
	capturingProvider
	providerName string
}

func (p *chatBackedResponsesProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	p.capturedResponsesReq = req
	return provideradapter.StreamResponsesViaChat(ctx, p, req, p.providerName)
}

func (c *capturingProvider) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	c.capturedEmbeddingReq = req
	if c.embeddingErr != nil {
		return nil, c.embeddingErr
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.embeddingResponse, nil
}

func TestStreamingResponses_ChatBackedProviderInjectsUsageWhenEnforced(t *testing.T) {
	streamData := strings.Join([]string{
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	provider := &chatBackedResponsesProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"gpt-4o-mini"},
				streamData:      streamData,
			},
		},
		providerName: "gemini",
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if provider.capturedResponsesReq == nil {
		t.Fatal("capturedResponsesReq = nil")
	}
	if provider.capturedResponsesReq.StreamOptions != nil {
		t.Fatalf("responses request was mutated: %+v", provider.capturedResponsesReq.StreamOptions)
	}
	if provider.capturedChatReq == nil {
		t.Fatal("capturedChatReq = nil")
	}
	if provider.capturedChatReq.StreamOptions == nil || !provider.capturedChatReq.StreamOptions.IncludeUsage {
		t.Fatalf("captured chat StreamOptions = %+v, want include_usage=true", provider.capturedChatReq.StreamOptions)
	}
}

func TestStreamingResponses_ChatBackedProviderDoesNotInjectUsageWhenDisabled(t *testing.T) {
	provider := &chatBackedResponsesProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"gpt-4o-mini"},
				streamData:      "data: [DONE]\n\n",
			},
		},
		providerName: "gemini",
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: false,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if provider.capturedChatReq == nil {
		t.Fatal("capturedChatReq = nil")
	}
	if provider.capturedChatReq.StreamOptions != nil {
		t.Fatalf("captured chat StreamOptions = %+v, want nil", provider.capturedChatReq.StreamOptions)
	}
}

func TestStreamingResponses_NativeProviderRequestRemainsUnchanged(t *testing.T) {
	streamData := "data: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
			streamData:      streamData,
		},
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if provider.capturedResponsesReq == nil {
		t.Fatal("capturedResponsesReq = nil")
	}
	if provider.capturedResponsesReq.StreamOptions != nil {
		t.Fatalf("native responses request should remain unchanged, got %+v", provider.capturedResponsesReq.StreamOptions)
	}
}

func TestStreamingResponses_ChatBackedProviderWritesExactlyOneUsageEntry(t *testing.T) {
	streamData := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"gemini-2.0-flash","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"chatcmpl-1","model":"gemini-2.0-flash","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	provider := &chatBackedResponsesProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"gemini-2.0-flash"},
				streamData:      streamData,
			},
		},
		providerName: "gemini",
	}
	usageLog := &collectingUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gemini-2.0-flash","input":"Hello","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-stream-responses-1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(usageLog.entries) != 1 {
		t.Fatalf("expected exactly 1 usage entry, got %d", len(usageLog.entries))
	}
	entry := usageLog.entries[0]
	if entry.RequestID != "req-stream-responses-1" {
		t.Fatalf("RequestID = %q, want req-stream-responses-1", entry.RequestID)
	}
	if entry.Provider != "mock" {
		t.Fatalf("Provider = %q, want mock", entry.Provider)
	}
	if entry.Endpoint != "/v1/responses" {
		t.Fatalf("Endpoint = %q, want /v1/responses", entry.Endpoint)
	}
	if entry.InputTokens != 11 || entry.OutputTokens != 5 || entry.TotalTokens != 16 {
		t.Fatalf("usage entry = %+v, want 11/5/16 tokens", entry)
	}
}

func TestResponses_PreservesUnknownNestedFields(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-5-mini"},
			responsesResponse: &core.ResponsesResponse{
				ID:        "resp_123",
				Object:    "response",
				CreatedAt: 1234567890,
				Model:     "gpt-5-mini",
				Status:    "completed",
				Output: []core.ResponsesOutputItem{
					{
						ID:     "msg_123",
						Type:   "message",
						Role:   "assistant",
						Status: "completed",
						Content: []core.ResponsesContentItem{
							{Type: "output_text", Text: "ok"},
						},
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{
		"model":"gpt-5-mini",
		"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.Responses(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if provider.capturedResponsesReq == nil {
		t.Fatal("expected responses request to be captured")
	}

	input, ok := provider.capturedResponsesReq.Input.([]core.ResponsesInputElement)
	if !ok || len(input) != 1 {
		t.Fatalf("captured input = %#v, want []ResponsesInputElement len=1", provider.capturedResponsesReq.Input)
	}
	if input[0].ExtraFields.Lookup("x_trace") == nil {
		t.Fatal("input[0].x_trace missing from ExtraFields")
	}

	body, err := json.Marshal(provider.capturedResponsesReq)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	decodedInput := decoded["input"].([]any)
	firstInput := decodedInput[0].(map[string]any)
	if _, ok := firstInput["x_trace"].(map[string]any); !ok {
		t.Fatalf("input[0].x_trace = %#v, want object", firstInput["x_trace"])
	}
}

func TestStreamingChatCompletion_InjectsStreamOptions(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"gpt-4o-mini"},
			providerTypes: map[string]string{
				"gpt-4o-mini": "openai",
			},
			streamData: streamData,
		},
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)

	// Streaming ChatCompletion request SHOULD have StreamOptions injected
	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.ChatCompletion(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if provider.lastPassthroughReq != nil {
		t.Fatal("lastPassthroughReq != nil, want usage-enforced streaming to stay on translated stream path")
	}

	if provider.capturedChatReq.StreamOptions == nil {
		t.Fatal("ChatCompletion streaming should have StreamOptions injected")
	}

	if !provider.capturedChatReq.StreamOptions.IncludeUsage {
		t.Error("ChatCompletion streaming should have IncludeUsage=true")
	}
}

func TestCreateFile(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o-mini", Object: "model"},
			},
		},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "batch"); err != nil {
		t.Fatalf("write purpose: %v", err)
	}
	part, err := writer.CreateFormFile("file", "requests.jsonl")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("{\"custom_id\":\"1\"}\n")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/files", nil, nil, nil, writer.FormDataContentType(), nil, false, "", nil)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.CreateFile(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"object\":\"file\"") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedFileRouteInfo() == nil {
		t.Fatal("expected file semantic envelope to be populated")
	}
	if env.CachedFileRouteInfo().Purpose != "batch" {
		t.Fatalf("purpose = %q, want batch", env.CachedFileRouteInfo().Purpose)
	}
	if env.CachedFileRouteInfo().Filename != "requests.jsonl" {
		t.Fatalf("filename = %q, want requests.jsonl", env.CachedFileRouteInfo().Filename)
	}
}

func TestCreateFileWithExplicitProviderDoesNotRequireProviderInventory(t *testing.T) {
	base := &mockProvider{
		fileCreateResponse: &core.FileObject{
			ID:        "file_ok_1",
			Object:    "file",
			Bytes:     16,
			CreatedAt: 1000,
			Filename:  "requests.jsonl",
			Purpose:   "batch",
			Provider:  "openai",
		},
	}

	provider := &providerWithoutFileInventory{inner: base}
	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "batch"); err != nil {
		t.Fatalf("write purpose: %v", err)
	}
	if err := writer.WriteField("provider", "openai"); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	part, err := writer.CreateFormFile("file", "requests.jsonl")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("{\"custom_id\":\"1\"}\n")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/files", nil, nil, nil, writer.FormDataContentType(), nil, false, "", nil)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.CreateFile(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if len(base.capturedFileCreateReqs) != 1 {
		t.Fatalf("len(capturedFileCreateReqs) = %d, want 1", len(base.capturedFileCreateReqs))
	}
}

func TestGetDeleteAndContentFile(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o-mini", Object: "model"},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	// Get file
	getReq := httptest.NewRequest(http.MethodGet, "/v1/files/file_1", nil)
	getRec := httptest.NewRecorder()
	getCtx := e.NewContext(getReq, getRec)
	getCtx.SetPath("/v1/files/:id")
	setPathParam(getCtx, "id", "file_1")
	if err := handler.GetFile(getCtx); err != nil {
		t.Fatalf("get handler returned error: %v", err)
	}
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d", getRec.Code)
	}

	// Delete file
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/files/file_1", nil)
	delRec := httptest.NewRecorder()
	delCtx := e.NewContext(delReq, delRec)
	delCtx.SetPath("/v1/files/:id")
	setPathParam(delCtx, "id", "file_1")
	if err := handler.DeleteFile(delCtx); err != nil {
		t.Fatalf("delete handler returned error: %v", err)
	}
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected delete status 200, got %d", delRec.Code)
	}

	// Get file content
	contentReq := httptest.NewRequest(http.MethodGet, "/v1/files/file_1/content", nil)
	contentRec := httptest.NewRecorder()
	contentCtx := e.NewContext(contentReq, contentRec)
	contentCtx.SetPath("/v1/files/:id/content")
	setPathParam(contentCtx, "id", "file_1")
	if err := handler.GetFileContent(contentCtx); err != nil {
		t.Fatalf("content handler returned error: %v", err)
	}
	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected content status 200, got %d", contentRec.Code)
	}
	if !strings.Contains(contentRec.Body.String(), "\"ok\":true") {
		t.Fatalf("unexpected content body: %s", contentRec.Body.String())
	}
}

func TestGetFileContent_TypedNilResponseReturnsBadGateway(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		fileContentByProv: map[string]*core.FileContentResponse{
			"openai": nil,
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files/file_1/content?provider=openai", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/files/:id/content")
	setPathParam(c, "id", "file_1")

	if err := handler.GetFileContent(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "provider_error") {
		t.Fatalf("expected provider_error body, got: %s", body)
	}
	if !strings.Contains(body, "provider returned empty file content response") {
		t.Fatalf("expected empty file content response message, got: %s", body)
	}
}

func TestListFiles(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku", "gemini-2.5-flash"},
		providerTypes: map[string]string{
			"gpt-4o-mini":      "openai",
			"claude-3-haiku":   "anthropic",
			"gemini-2.5-flash": "gemini",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o-mini", Object: "model"},
				{ID: "claude-3-haiku", Object: "model"},
				{ID: "gemini-2.5-flash", Object: "model"},
			},
		},
		fileListByProvider: map[string]*core.FileListResponse{
			"openai": {
				Object: "list",
				Data: []core.FileObject{
					{
						ID:        "file_ok_1",
						Object:    "file",
						Bytes:     10,
						CreatedAt: 1000,
						Filename:  "a.jsonl",
						Purpose:   "batch",
						Provider:  "openai",
					},
				},
			},
		},
		fileErrByProvider: map[string]error{
			"anthropic": core.NewNotFoundError(""),
			"gemini":    core.NewProviderError("gemini", http.StatusUnauthorized, "Not available for your plan", nil),
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files?limit=5", nil)
	frame := core.NewRequestSnapshot(
		http.MethodGet,
		"/v1/files",
		nil,
		map[string][]string{
			"limit": {"5"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ListFiles(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"object\":\"list\"") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"id\":\"file_ok_1\"") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
	env := core.GetWhiteBoxPrompt(c.Request().Context())
	if env == nil || env.CachedFileRouteInfo() == nil {
		t.Fatal("expected file semantic envelope to be populated")
	}
	if !env.CachedFileRouteInfo().HasLimit || env.CachedFileRouteInfo().Limit != 5 {
		t.Fatalf("limit = %d/%v, want 5/true", env.CachedFileRouteInfo().Limit, env.CachedFileRouteInfo().HasLimit)
	}
}

func TestListFilesWithUnknownAfterCursorReturnsNotFound(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o-mini", Object: "model"},
			},
		},
		fileListByProvider: map[string]*core.FileListResponse{
			"openai": {
				Object: "list",
				Data: []core.FileObject{
					{
						ID:        "file_ok_1",
						Object:    "file",
						Bytes:     10,
						CreatedAt: 1000,
						Filename:  "a.jsonl",
						Purpose:   "batch",
						Provider:  "openai",
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files?after=missing-cursor", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ListFiles(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "after cursor file not found") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
}

func TestListFilesWithoutProviderPagesProvidersUntilAfterCursor(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o-mini":    "openai",
			"claude-3-haiku": "anthropic",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o-mini", Object: "model"},
				{ID: "claude-3-haiku", Object: "model"},
			},
		},
		fileListPagesByProvider: map[string]map[string]*core.FileListResponse{
			"openai": {
				"": {
					Object: "list",
					Data: []core.FileObject{
						{ID: "file_o6", Object: "file", CreatedAt: 106, Provider: "openai"},
						{ID: "file_o5", Object: "file", CreatedAt: 105, Provider: "openai"},
						{ID: "file_o4", Object: "file", CreatedAt: 104, Provider: "openai"},
					},
					HasMore: true,
				},
				"file_o4": {
					Object: "list",
					Data: []core.FileObject{
						{ID: "file_o3", Object: "file", CreatedAt: 100, Provider: "openai"},
						{ID: "file_o2", Object: "file", CreatedAt: 99, Provider: "openai"},
						{ID: "file_o1", Object: "file", CreatedAt: 98, Provider: "openai"},
					},
				},
			},
			"anthropic": {
				"": {
					Object: "list",
					Data: []core.FileObject{
						{ID: "file_a3", Object: "file", CreatedAt: 103, Provider: "anthropic"},
						{ID: "file_a2", Object: "file", CreatedAt: 102, Provider: "anthropic"},
						{ID: "file_a1", Object: "file", CreatedAt: 101, Provider: "anthropic"},
					},
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files?limit=2&after=file_a1", nil)
	frame := core.NewRequestSnapshot(
		http.MethodGet,
		"/v1/files",
		nil,
		map[string][]string{
			"limit": {"2"},
			"after": {"file_a1"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ListFiles(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp core.FileListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got := []string{resp.Data[0].ID, resp.Data[1].ID}; !reflect.DeepEqual(got, []string{"file_o3", "file_o2"}) {
		t.Fatalf("response ids = %v, want [file_o3 file_o2]", got)
	}
	if !resp.HasMore {
		t.Fatal("HasMore = false, want true")
	}
	if len(mock.fileListCalls) < 3 {
		t.Fatalf("len(fileListCalls) = %d, want at least 3", len(mock.fileListCalls))
	}
	lastCall := mock.fileListCalls[len(mock.fileListCalls)-1]
	if lastCall.provider != "openai" || lastCall.after != "file_o4" {
		t.Fatalf("last file list call = %#v, want openai after file_o4", lastCall)
	}
}

func TestGetFileWithoutProviderSkipsProviderErrors(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku", "gemini-2.5-flash"},
		providerTypes: map[string]string{
			"gpt-4o-mini":      "openai",
			"claude-3-haiku":   "anthropic",
			"gemini-2.5-flash": "gemini",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o-mini", Object: "model"},
				{ID: "claude-3-haiku", Object: "model"},
				{ID: "gemini-2.5-flash", Object: "model"},
			},
		},
		fileErrByProvider: map[string]error{
			"anthropic": core.NewNotFoundError(""),
			"gemini":    core.NewProviderError("gemini", http.StatusUnauthorized, "Not available for your plan", nil),
		},
		fileGetByProvider: map[string]*core.FileObject{
			"openai": {
				ID:        "file_ok_1",
				Object:    "file",
				Bytes:     10,
				CreatedAt: 1000,
				Filename:  "a.jsonl",
				Purpose:   "batch",
				Provider:  "openai",
			},
		},
	}

	e := echo.New()
	handler := NewHandler(mock, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files/file_ok_1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/files/:id")
	setPathParam(c, "id", "file_ok_1")
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c.Set(string(auditlog.LogEntryKey), entry)

	if err := handler.GetFile(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"id\":\"file_ok_1\"") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
	if entry.Provider != "openai" {
		t.Fatalf("audit entry provider = %q, want openai", entry.Provider)
	}
}

func TestGetFileWithoutProviderUsesProviderInventoryWhenAliasMasksModel(t *testing.T) {
	catalog := aliasesTestCatalog{
		supported: map[string]bool{
			"gpt-4o":         true,
			"claude-3-haiku": true,
		},
		providerTypes: map[string]string{
			"gpt-4o":         "openai",
			"claude-3-haiku": "anthropic",
		},
		models: map[string]core.Model{
			"gpt-4o":         {ID: "gpt-4o", Object: "model"},
			"claude-3-haiku": {ID: "claude-3-haiku", Object: "model"},
		},
	}

	service, err := aliases.NewService(newAliasesTestStore(
		aliases.Alias{Name: "gpt-4o", TargetModel: "claude-3-haiku", Enabled: true},
	), &catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	mock := &mockProvider{
		supportedModels: []string{"gpt-4o", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o":         "openai",
			"claude-3-haiku": "anthropic",
		},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model"},
				{ID: "claude-3-haiku", Object: "model"},
			},
		},
		fileErrByProvider: map[string]error{
			"anthropic": core.NewNotFoundError(""),
		},
		fileGetByProvider: map[string]*core.FileObject{
			"openai": {
				ID:        "file_ok_1",
				Object:    "file",
				Bytes:     10,
				CreatedAt: 1000,
				Filename:  "a.jsonl",
				Purpose:   "batch",
				Provider:  "openai",
			},
		},
	}

	provider := aliases.NewProvider(mock, service)
	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files/file_ok_1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/files/:id")
	setPathParam(c, "id", "file_ok_1")

	if err := handler.GetFile(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"provider\":\"openai\"") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
}

func TestGetFileWithoutProviderRequiresFileProviderInventory(t *testing.T) {
	base := &mockProvider{
		supportedModels: []string{"gpt-4o"},
		providerTypes: map[string]string{
			"gpt-4o": "openai",
		},
		fileGetByProvider: map[string]*core.FileObject{
			"openai": {
				ID:        "file_ok_1",
				Object:    "file",
				Bytes:     10,
				CreatedAt: 1000,
				Filename:  "a.jsonl",
				Purpose:   "batch",
				Provider:  "openai",
			},
		},
	}

	provider := &providerWithoutFileInventory{inner: base}
	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files/file_ok_1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/files/:id")
	setPathParam(c, "id", "file_ok_1")

	if err := handler.GetFile(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "provider_error") {
		t.Fatalf("expected provider_error body, got: %s", body)
	}
	if !strings.Contains(body, "file provider inventory is unavailable") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestMergeStoredBatchFromUpstreamPreservesGatewayMetadata(t *testing.T) {
	stored := &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:              "batch_1",
			Provider:        "openai",
			ProviderBatchID: "provider-batch-1",
			InputFileID:     "file_source",
			Metadata: map[string]string{
				"provider":          "openai",
				"provider_batch_id": "provider-batch-1",
				"existing":          "keep-me",
			},
		},
		OriginalInputFileID:  "file_source",
		RewrittenInputFileID: "file_hidden",
	}
	upstream := &core.BatchResponse{
		Status:      "completed",
		InputFileID: "file_hidden",
		Metadata: map[string]string{
			"provider":          "anthropic",
			"provider_batch_id": "other-id",
			"existing":          "upstream-overwrite",
			"new_key":           "new-value",
		},
	}

	mergeStoredBatchFromUpstream(stored, upstream)

	if stored.Batch.Metadata["provider"] != "openai" {
		t.Fatalf("provider metadata overwritten: %q", stored.Batch.Metadata["provider"])
	}
	if stored.Batch.Metadata["provider_batch_id"] != "provider-batch-1" {
		t.Fatalf("provider_batch_id metadata overwritten: %q", stored.Batch.Metadata["provider_batch_id"])
	}
	if stored.Batch.Metadata["existing"] != "upstream-overwrite" {
		t.Fatalf("expected non-gateway key overwrite from upstream, got %q", stored.Batch.Metadata["existing"])
	}
	if stored.Batch.Metadata["new_key"] != "new-value" {
		t.Fatalf("expected merged upstream key, got %q", stored.Batch.Metadata["new_key"])
	}
	if stored.Batch.InputFileID != "file_source" {
		t.Fatalf("input_file_id overwritten: %q", stored.Batch.InputFileID)
	}
}

func TestMergeStoredBatchFromUpstreamPreservesExistingValuesOnSparseUpstream(t *testing.T) {
	inProgressAt := int64(1001)
	completedAt := int64(1002)
	inputCost := 0.11
	totalCost := 0.22

	stored := &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			ID:               "batch_1",
			Status:           "in_progress",
			Endpoint:         "/v1/chat/completions",
			InputFileID:      "file_source",
			CompletionWindow: "24h",
			RequestCounts: core.BatchRequestCounts{
				Total:     10,
				Completed: 4,
				Failed:    1,
			},
			Usage: core.BatchUsageSummary{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
				InputCost:    &inputCost,
				TotalCost:    &totalCost,
			},
			Results: []core.BatchResultItem{
				{Index: 0, CustomID: "keep-me", StatusCode: 200},
			},
			InProgressAt: &inProgressAt,
			CompletedAt:  &completedAt,
			Metadata: map[string]string{
				"provider": "openai",
			},
		},
		OriginalInputFileID: "file_source",
	}

	upstream := &core.BatchResponse{
		Status:        "",
		Endpoint:      "",
		InputFileID:   "",
		RequestCounts: core.BatchRequestCounts{},
		Usage:         core.BatchUsageSummary{},
		Results:       nil,
		Metadata: map[string]string{
			"upstream_only": "value",
		},
	}

	mergeStoredBatchFromUpstream(stored, upstream)

	if got := stored.Batch.Status; got != "in_progress" {
		t.Fatalf("Status = %q, want in_progress", got)
	}
	if got := stored.Batch.Endpoint; got != "/v1/chat/completions" {
		t.Fatalf("Endpoint = %q, want /v1/chat/completions", got)
	}
	if got := stored.Batch.InputFileID; got != "file_source" {
		t.Fatalf("InputFileID = %q, want file_source", got)
	}
	if got := stored.Batch.CompletionWindow; got != "24h" {
		t.Fatalf("CompletionWindow = %q, want 24h", got)
	}
	if got := stored.Batch.RequestCounts; got != (core.BatchRequestCounts{Total: 10, Completed: 4, Failed: 1}) {
		t.Fatalf("RequestCounts = %#v, want preserved counts", got)
	}
	if got := stored.Batch.Usage; got.InputTokens != 100 || got.OutputTokens != 50 || got.TotalTokens != 150 || got.InputCost == nil || *got.InputCost != inputCost || got.TotalCost == nil || *got.TotalCost != totalCost {
		t.Fatalf("Usage = %#v, want preserved usage", got)
	}
	if len(stored.Batch.Results) != 1 || stored.Batch.Results[0].CustomID != "keep-me" {
		t.Fatalf("Results = %#v, want preserved results", stored.Batch.Results)
	}
	if stored.Batch.InProgressAt == nil || *stored.Batch.InProgressAt != inProgressAt {
		t.Fatalf("InProgressAt = %#v, want %d", stored.Batch.InProgressAt, inProgressAt)
	}
	if stored.Batch.CompletedAt == nil || *stored.Batch.CompletedAt != completedAt {
		t.Fatalf("CompletedAt = %#v, want %d", stored.Batch.CompletedAt, completedAt)
	}
	if got := stored.Batch.Metadata["upstream_only"]; got != "value" {
		t.Fatalf("metadata[upstream_only] = %q, want value", got)
	}
}

func TestProviderPassthrough_OpenAI(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusAccepted,
			Headers: map[string][]string{
				"Content-Type":   {"application/json"},
				"X-Upstream":     {"openai"},
				"Set-Cookie":     {"session=secret"},
				"Connection":     {"X-Upstream-Hop, Keep-Alive"},
				"X-Upstream-Hop": {"secret"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses?api-version=2026-03-10", strings.NewReader(`{"foo":"bar"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer user-secret")
	req.Header.Set("Cookie", "session=user-secret")
	req.Header.Set("Forwarded", "for=10.0.0.1")
	req.Header.Set("OpenAI-Beta", "responses=v1")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("Connection", "X-Debug, keep-alive")
	req.Header.Set("X-Debug", "secret")
	req.Header.Set("X-Request-ID", "req_123")
	req.Header.Set(core.UserPathHeader, "/team/a/user")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := rec.Body.String(); got != `{"ok":true}` {
		t.Fatalf("body = %q", got)
	}
	if got := rec.Header().Get("X-Upstream"); got != "openai" {
		t.Fatalf("X-Upstream = %q, want openai", got)
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie should not be forwarded, got %q", got)
	}
	if got := rec.Header().Get("X-Upstream-Hop"); got != "" {
		t.Fatalf("hop-by-hop header should not be forwarded, got %q", got)
	}
	if provider.lastPassthroughProvider != "openai" {
		t.Fatalf("providerType = %q, want openai", provider.lastPassthroughProvider)
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("lastPassthroughReq = nil")
	}
	if got := provider.lastPassthroughReq.Endpoint; got != "responses?api-version=2026-03-10" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := readPassthroughRequestBody(t, provider.lastPassthroughReq.Body); got != `{"foo":"bar"}` {
		t.Fatalf("body = %q", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("Authorization"); got != "" {
		t.Fatalf("authorization header should not be forwarded, got %q", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("Cookie"); got != "" {
		t.Fatalf("cookie header should not be forwarded, got %q", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("Forwarded"); got != "" {
		t.Fatalf("forwarded header should not be forwarded, got %q", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("x-forwarded-for header should not be forwarded, got %q", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("X-Debug"); got != "" {
		t.Fatalf("connection-nominated header should not be forwarded, got %q", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("OpenAI-Beta"); got != "responses=v1" {
		t.Fatalf("OpenAI-Beta = %q, want responses=v1", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get("X-Request-ID"); got != "req_123" {
		t.Fatalf("X-Request-ID = %q, want req_123", got)
	}
	if got := provider.lastPassthroughReq.Headers.Get(core.UserPathHeader); got != "" {
		t.Fatalf("%s should not be forwarded, got %q", core.UserPathHeader, got)
	}
}

func TestProviderPassthrough_PrefersContextRequestID(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{}`))
	req = req.WithContext(core.WithRequestID(req.Context(), "ctx_req_123"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "header_req_456")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("lastPassthroughReq = nil")
	}
	if got := provider.lastPassthroughReq.Headers.Get("X-Request-ID"); got != "ctx_req_123" {
		t.Fatalf("X-Request-ID = %q, want ctx_req_123", got)
	}
}

func TestProviderPassthrough_NormalizesErrorResponse(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusNotFound,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
				"X-Upstream":   {"openai"},
			},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"upstream missing","type":"invalid_request_error"}}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("X-Upstream"); got != "" {
		t.Fatalf("X-Upstream should not be forwarded on normalized errors, got %q", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"message":"upstream missing"`) || !strings.Contains(body, `"error"`) {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestProviderPassthrough_OpenAIV1AliasNormalizesByDefault(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("lastPassthroughReq = nil")
	}
	if got := provider.lastPassthroughReq.Endpoint; got != "chat/completions" {
		t.Fatalf("endpoint = %q, want chat/completions", got)
	}
}

func TestProviderPassthrough_AnthropicV1AliasNormalizesByDefault(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("lastPassthroughReq = nil")
	}
	if got := provider.lastPassthroughReq.Endpoint; got != "messages" {
		t.Fatalf("endpoint = %q, want messages", got)
	}
}

func TestProviderPassthrough_UsesPassthroughModelForAuditEntry(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai",
			RawEndpoint:        "chat/completions",
			NormalizedEndpoint: "chat/completions",
			Model:              "gpt-5-mini",
			AuditPath:          "/v1/chat/completions",
		},
	}))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)

	if err := handler.ProviderPassthrough(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if entry.RequestedModel != "gpt-5-mini" {
		t.Fatalf("audit entry requested model = %q, want gpt-5-mini", entry.RequestedModel)
	}
	if entry.Provider != "openai" {
		t.Fatalf("audit entry provider = %q, want openai", entry.Provider)
	}
}

func TestProviderPassthrough_UsesConfiguredProviderNameForAccessValidation(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
		providerTypes: map[string]string{
			"openai_test/gpt-5-mini": "openai",
		},
		providerNames: map[string]string{
			"openai_test/gpt-5-mini": "openai_test",
		},
	}
	authorizer := &recordingModelAuthorizer{}

	e := echo.New()
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/p/openai_test/chat/completions", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai_test",
			RawEndpoint:        "chat/completions",
			NormalizedEndpoint: "chat/completions",
			Model:              "gpt-5-mini",
			AuditPath:          "/p/openai_test/chat/completions",
		},
	}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ProviderPassthrough(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if provider.lastPassthroughProvider != "openai" {
		t.Fatalf("providerType = %q, want openai", provider.lastPassthroughProvider)
	}
	if authorizer.lastSelector.Provider != "openai_test" || authorizer.lastSelector.Model != "gpt-5-mini" {
		t.Fatalf("validated selector = %#v, want openai_test/gpt-5-mini", authorizer.lastSelector)
	}
}

func TestProviderPassthrough_FallsBackFromProviderTypeToCanonicalProviderNameForAccessValidation(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
		providerTypes: map[string]string{
			"openai_test/gpt-5-mini": "openai",
		},
		providerNames: map[string]string{
			"openai_test/gpt-5-mini": "openai_test",
		},
	}
	authorizer := &recordingModelAuthorizer{}

	e := echo.New()
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai",
			RawEndpoint:        "chat/completions",
			NormalizedEndpoint: "chat/completions",
			Model:              "gpt-5-mini",
			AuditPath:          "/p/openai/chat/completions",
		},
	}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := handler.ProviderPassthrough(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if provider.lastPassthroughProvider != "openai" {
		t.Fatalf("providerType = %q, want openai", provider.lastPassthroughProvider)
	}
	if authorizer.lastSelector.Provider != "openai_test" || authorizer.lastSelector.Model != "gpt-5-mini" {
		t.Fatalf("validated selector = %#v, want openai_test/gpt-5-mini", authorizer.lastSelector)
	}
}

func TestProviderPassthrough_V1AliasDisabledReturnsBadRequest(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	handler.normalizePassthroughV1Prefix = false
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "v1 alias is disabled") {
		t.Fatalf("body = %q, want v1 alias error", rec.Body.String())
	}
	if provider.lastPassthroughReq != nil {
		t.Fatalf("provider should not have been called, got endpoint %q", provider.lastPassthroughReq.Endpoint)
	}
}

func TestProviderPassthrough_AnthropicStream(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body: &chunkedReadCloser{
				chunks: [][]byte{
					[]byte("event: message_start\n"),
					[]byte("data: {\"type\":\"message_start\"}\n\n"),
				},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/anthropic/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
	req.Header.Set("Content-Type", "application/json")

	rec := &flushCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q", got)
	}
	if rec.flushes == 0 {
		t.Fatal("expected streaming response to flush")
	}
	if got := rec.Body.String(); !strings.Contains(got, "message_start") {
		t.Fatalf("unexpected stream body: %q", got)
	}
}

func TestProviderPassthrough_StreamWithoutObserversClosesUpstreamBodyOnce(t *testing.T) {
	body := &closeCountingReadCloser{
		ReadCloser: &chunkedReadCloser{
			chunks: [][]byte{
				[]byte("event: message_start\n"),
				[]byte("data: {\"type\":\"message_start\"}\n\n"),
			},
		},
	}
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body: body,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/anthropic/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
	req.Header.Set("Content-Type", "application/json")

	rec := &flushCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.closes != 1 {
		t.Fatalf("Close calls = %d, want 1", body.closes)
	}
}

func TestProviderPassthrough_OpenAIStreamWritesUsageEntry(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"resp-123\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	usageLog := &collectingUsageLogger{
		config: usage.Config{Enabled: true},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-pass-stream-usage")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(usageLog.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLog.entries))
	}

	entry := usageLog.entries[0]
	if entry.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", entry.Provider)
	}
	if entry.Endpoint != "/p/openai/responses" {
		t.Fatalf("Endpoint = %q, want /p/openai/responses", entry.Endpoint)
	}
	if entry.Model != "gpt-5-mini" {
		t.Fatalf("Model = %q, want gpt-5-mini", entry.Model)
	}
	if entry.TotalTokens != 10 {
		t.Fatalf("TotalTokens = %d, want 10", entry.TotalTokens)
	}
	if entry.RequestID != "req-pass-stream-usage" {
		t.Fatalf("RequestID = %q, want req-pass-stream-usage", entry.RequestID)
	}
}

func TestProviderPassthrough_OpenAIStreamUsageKeepsClientVisibleRoute(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"resp-123\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	usageLog := &collectingUsageLogger{
		config: usage.Config{Enabled: true},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-pass-stream-visible-path")
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai",
			RawEndpoint:        "v1/responses",
			NormalizedEndpoint: "responses",
			AuditPath:          "/v1/responses",
			Model:              "gpt-5-mini",
		},
	}))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(usageLog.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLog.entries))
	}
	if got := usageLog.entries[0].Endpoint; got != "/p/openai/v1/responses" {
		t.Fatalf("Endpoint = %q, want /p/openai/v1/responses", got)
	}
}

func TestPassthroughStreamAuditPath_NormalizesKnownEndpoints(t *testing.T) {
	tests := []struct {
		name        string
		requestPath string
		provider    string
		endpoint    string
		want        string
	}{
		{
			name:        "openai responses",
			requestPath: "/p/openai/responses",
			provider:    "openai",
			endpoint:    "responses?trace=1",
			want:        "/v1/responses",
		},
		{
			name:        "anthropic messages",
			requestPath: "/p/anthropic/messages",
			provider:    "anthropic",
			endpoint:    "messages",
			want:        "/v1/messages",
		},
		{
			name:        "unknown endpoint falls back",
			requestPath: "/p/openai/unknown",
			provider:    "openai",
			endpoint:    "unknown",
			want:        "/p/openai/unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passthroughStreamAuditPath(tt.requestPath, tt.provider, tt.endpoint); got != tt.want {
				t.Fatalf("passthroughStreamAuditPath(%q, %q, %q) = %q, want %q", tt.requestPath, tt.provider, tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestProviderPassthrough_RejectsUnsupportedProvider(t *testing.T) {
	provider := &mockProvider{}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/groq/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `provider passthrough for \"groq\" is not enabled`) {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "anthropic, openai, openrouter, vllm, zai") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestProviderPassthrough_UsesConfiguredSupportedProviders(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	handler.setEnabledPassthroughProviders([]string{"groq"})
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/groq/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if provider.lastPassthroughProvider != "groq" {
		t.Fatalf("providerType = %q, want groq", provider.lastPassthroughProvider)
	}
	if provider.lastPassthroughReq == nil {
		t.Fatal("lastPassthroughReq = nil")
	}
	if got := provider.lastPassthroughReq.Endpoint; got != "chat/completions" {
		t.Fatalf("endpoint = %q, want chat/completions", got)
	}
	if got := readPassthroughRequestBody(t, provider.lastPassthroughReq.Body); got != `{}` {
		t.Fatalf("body = %q, want {}", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestIsNativeBatchResultsPending(t *testing.T) {
	provider := &mockProvider{
		batchGetResponse: &core.BatchResponse{ID: "provider-batch-1", Status: "in_progress"},
	}
	anthropicErr := core.NewProviderError("anthropic", http.StatusNotFound, "pending", nil)
	pending, latest := isNativeBatchResultsPending(context.Background(), provider, "anthropic", "provider-batch-1", anthropicErr)
	if !pending {
		t.Fatal("expected anthropic 404 to be treated as pending")
	}
	if latest == nil || latest.Status != "in_progress" {
		t.Fatalf("latest = %#v, want in_progress batch", latest)
	}

	openAIErr := core.NewProviderError("openai", http.StatusNotFound, "not found", nil)
	if pending, _ := isNativeBatchResultsPending(context.Background(), provider, "openai", "provider-batch-1", openAIErr); pending {
		t.Fatal("expected openai 404 not to be treated as pending")
	}

	provider.batchGetResponse = &core.BatchResponse{ID: "provider-batch-1", Status: "expired"}
	if pending, _ := isNativeBatchResultsPending(context.Background(), provider, "anthropic", "provider-batch-1", anthropicErr); pending {
		t.Fatal("expected terminal anthropic batch not to be treated as pending")
	}
}
