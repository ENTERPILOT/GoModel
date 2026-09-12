package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/observability"
	"github.com/enterpilot/gomodel/internal/plugins"
	provideradapter "github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
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

// redirectVM builds a redirect (alias) virtual model for server tests.
func redirectVM(name, targetModel, targetProvider string, enabled bool) virtualmodels.VirtualModel {
	return virtualmodels.VirtualModel{
		Source:  name,
		Targets: []virtualmodels.Target{{Provider: targetProvider, Model: targetModel}},
		Enabled: enabled,
	}
}

type aliasesTestStore struct {
	rows []virtualmodels.VirtualModel
}

func newAliasesTestStore(rows ...virtualmodels.VirtualModel) *aliasesTestStore {
	return &aliasesTestStore{rows: append([]virtualmodels.VirtualModel(nil), rows...)}
}

func (s *aliasesTestStore) List(_ context.Context) ([]virtualmodels.VirtualModel, error) {
	return append([]virtualmodels.VirtualModel(nil), s.rows...), nil
}

func (s *aliasesTestStore) Get(_ context.Context, source string) (*virtualmodels.VirtualModel, error) {
	for _, vm := range s.rows {
		if vm.Source == source {
			clone := vm
			return &clone, nil
		}
	}
	return nil, virtualmodels.ErrNotFound
}

func (s *aliasesTestStore) Upsert(_ context.Context, vm virtualmodels.VirtualModel) error {
	for i := range s.rows {
		if s.rows[i].Source == vm.Source {
			s.rows[i] = vm
			return nil
		}
	}
	s.rows = append(s.rows, vm)
	return nil
}

func (s *aliasesTestStore) Delete(_ context.Context, source string) error {
	for i := range s.rows {
		if s.rows[i].Source == source {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return virtualmodels.ErrNotFound
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

func (c *aliasesTestCatalog) ModelAvailable(model string) bool {
	return c.Supports(model)
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

func (c *aliasesTestCatalog) ProviderNames() []string {
	seen := map[string]struct{}{}
	names := make([]string, 0, len(c.providerTypes))
	for _, providerType := range c.providerTypes {
		if providerType == "" {
			continue
		}
		if _, ok := seen[providerType]; ok {
			continue
		}
		seen[providerType] = struct{}{}
		names = append(names, providerType)
	}
	return names
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

func (s *failingBatchStore) List(context.Context, int, string, string) ([]*batchstore.StoredBatch, error) {
	return nil, nil
}

func (s *failingBatchStore) Update(context.Context, *batchstore.StoredBatch) error {
	return batchstore.ErrNotFound
}

func (s *failingBatchStore) Delete(context.Context, string) error {
	return batchstore.ErrNotFound
}

func (s *failingBatchStore) Close() error {
	return nil
}

type emptyProviderFileStore struct{}

func (emptyProviderFileStore) Upsert(context.Context, *filestore.StoredFile) error {
	return nil
}

func (emptyProviderFileStore) Get(_ context.Context, id string) (*filestore.StoredFile, error) {
	return &filestore.StoredFile{ID: id}, nil
}

func (emptyProviderFileStore) List(context.Context, filestore.ListFilter, int, string) ([]*filestore.StoredFile, error) {
	return nil, nil
}

func (emptyProviderFileStore) Delete(context.Context, string) error {
	return nil
}

func (emptyProviderFileStore) Close() error {
	return nil
}

type failingFileStore struct {
	err error
}

func (s failingFileStore) storeErr() error {
	if s.err != nil {
		return s.err
	}
	return errors.New("file store failed")
}

func (s failingFileStore) Upsert(context.Context, *filestore.StoredFile) error {
	return s.storeErr()
}

func (s failingFileStore) Get(context.Context, string) (*filestore.StoredFile, error) {
	return nil, s.storeErr()
}

func (s failingFileStore) List(context.Context, filestore.ListFilter, int, string) ([]*filestore.StoredFile, error) {
	return nil, s.storeErr()
}

func (s failingFileStore) Delete(context.Context, string) error {
	return s.storeErr()
}

func (s failingFileStore) Close() error {
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
	chatCompletionCalls     int
	embeddingCalls          int
	lastPassthroughProvider string
	lastPassthroughReq      *core.PassthroughRequest

	responseGetResponse         *core.ResponsesResponse
	responseGetHook             func()
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
	require.NoError(t, err)

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

func (m *mockProvider) NativeBatchProviderTypes() []string {
	return m.NativeFileProviderTypes()
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
	m.chatCompletionCalls++
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
	m.embeddingCalls++
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
	if m.passthroughResponse != nil {
		return m.passthroughResponse, nil
	}
	return nil, nil
}

func (m *mockProvider) GetResponse(_ context.Context, providerType, id string, _ core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	m.responseGetCalls = append(m.responseGetCalls, responseCall{provider: providerType, id: id})
	if m.responseGetHook != nil {
		m.responseGetHook()
	}
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
				Purpose:   gateway.FirstNonEmpty(purpose, "batch"),
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

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "chatcmpl-123")
	assert.Contains(t, body, "Hello!")
}

func TestChatCompletion_BindsMultimodalContent(t *testing.T) {
	provider := &capturingProvider{
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
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"}}]}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedChatReq)

	parts, ok := core.NormalizeContentParts(provider.capturedChatReq.Messages[0].Content)
	require.True(t, ok, "captured content type = %T, want structured content", provider.capturedChatReq.Messages[0].Content)
	require.Len(t, parts, 2)
	require.Equal(t, "text", parts[0].Type)
	require.Equal(t, "Describe this image", parts[0].Text)
	require.Equal(t, "image_url", parts[1].Type)
	require.NotNil(t, parts[1].ImageURL)
	require.Equal(t, "https://example.com/image.png", parts[1].ImageURL.URL)
}

func TestChatCompletion_PreservesUnknownTopLevelFields(t *testing.T) {
	provider := &capturingProvider{
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
	}

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
	c, _ := echotest.Post(t, "/v1/chat/completions", reqBody)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.NotNil(t, provider.capturedChatReq)
	require.NotNil(t, provider.capturedChatReq.ExtraFields.Lookup("response_format"))

	body, err := json.Marshal(provider.capturedChatReq)
	require.NoError(t, err)
	require.Contains(t, string(body), `"response_format"`)
}

func TestChatCompletion_PreservesUnknownNestedFields(t *testing.T) {
	provider := &capturingProvider{
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
	}

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
	c, _ := echotest.Post(t, "/v1/chat/completions", reqBody)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.NotNil(t, provider.capturedChatReq)
	require.NotNil(t, provider.capturedChatReq.Messages[0].ExtraFields.Lookup("name"))

	body, err := json.Marshal(provider.capturedChatReq)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	messages := decoded["messages"].([]any)
	firstMsg := messages[0].(map[string]any)
	require.Equal(t, "alice", firstMsg["name"])

	content := firstMsg["content"].([]any)
	firstPart := content[0].(map[string]any)
	_, ok := firstPart["cache_control"].(map[string]any)
	require.True(t, ok, "messages[0].content[0].cache_control = %#v, want object", firstPart["cache_control"])
}

func TestChatCompletion_UsesIngressFrameForDecoding(t *testing.T) {
	provider := &capturingProvider{
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
	}

	handler := NewHandler(provider, nil, nil, nil)

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
	c, rec := echotest.Post(t, "/v1/chat/completions", &explodingReadCloser{}, echotest.WithHeader("X-Request-ID", "req-ingress-1"))
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedChatReq)
	require.NotNil(t, provider.capturedChatReq.ExtraFields.Lookup("response_format"))

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedChatRequest())
	require.Equal(t, provider.capturedChatReq, env.CachedChatRequest())
}

func TestChatCompletion_NormalizesSemanticSelectorHints(t *testing.T) {
	provider := &capturingProvider{
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
	}

	handler := NewHandler(provider, nil, nil, nil)

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
	c, rec := echotest.Post(t, "/v1/chat/completions", &explodingReadCloser{})
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedChatReq)
	require.Equal(t, "gpt-5-mini", provider.capturedChatReq.Model)
	require.Equal(t, "openai", provider.capturedChatReq.Provider)

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedChatRequest())
	require.Equal(t, "gpt-5-mini", env.RouteHints.Model)
	require.Equal(t, "openai", env.RouteHints.Provider)
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

	service, err := virtualmodels.NewService(newAliasesTestStore(
		redirectVM("anthropic/claude-opus-4-6", "gpt-5-nano", "openai", true),
	), &catalog, true)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	inner := &capturingProvider{
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
	}

	handler := newHandler(inner, nil, nil, nil, service, nil, nil, nil)

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
	c, rec := echotest.Post(t, "/v1/chat/completions", &explodingReadCloser{})
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))

	err = handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, inner.capturedChatReq)
	require.Equal(t, "gpt-5-nano", inner.capturedChatReq.Model)
	require.Equal(t, "openai", inner.capturedChatReq.Provider)

	workflow := core.GetWorkflow(c.Request().Context())
	require.NotNil(t, workflow)
	require.NotNil(t, workflow.Resolution)
	require.True(t, workflow.Resolution.AliasApplied)
	require.Equal(t, "openai/gpt-5-nano", workflow.ResolvedQualifiedModel())
}

func TestChatCompletion_UsesExplicitTranslatedRequestPatcher(t *testing.T) {
	chains := newSystemPromptChains(t, "guardrail system")

	inner := &capturingProvider{
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
	}

	patcher := guardrails.NewWorkflowRequestPatcher(staticChainsResolver{chains: chains})

	handler := newHandler(inner, nil, nil, nil, nil, nil, nil, patcher)

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
	c, rec := echotest.Post(t, "/v1/chat/completions", &explodingReadCloser{})
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, inner.capturedChatReq)
	require.Len(t, inner.capturedChatReq.Messages, 2)
	require.Equal(t, "system", inner.capturedChatReq.Messages[0].Role)
	require.Equal(t, "guardrail system", inner.capturedChatReq.Messages[0].Content, "first message = %+v, want injected guardrail system prompt", inner.capturedChatReq.Messages[0])
	require.Equal(t, "user", inner.capturedChatReq.Messages[1].Role)
}

func TestBatches_UsesExplicitGuardrailBatchPreparer(t *testing.T) {
	chains := newSystemPromptChains(t, "guardrail system")

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
	batchPreparer := guardrails.NewWorkflowBatchPreparer(mock, staticChainsResolver{chains: chains})

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
	c, rec := echotest.Post(t, "/v1/batches", reqBody)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, mock.capturedBatchReq)
	require.Len(t, mock.capturedBatchReq.Requests, 1)

	var chatReq core.ChatRequest
	err = json.Unmarshal(mock.capturedBatchReq.Requests[0].Body, &chatReq)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 2)
	require.Equal(t, "system", chatReq.Messages[0].Role)
	require.Equal(t, "guardrail system", chatReq.Messages[0].Content, "first batch message = %+v, want injected guardrail system prompt", chatReq.Messages[0])
}

func TestResponses_UsesIngressFrameForDecoding(t *testing.T) {
	provider := &capturingProvider{
		supportedModels: []string{"gpt-5-mini"},
		responsesResponse: &core.ResponsesResponse{
			ID:        "resp_123",
			Object:    "response",
			CreatedAt: 1234567890,
			Model:     "gpt-5-mini",
			Status:    "completed",
		},
	}

	handler := NewHandler(provider, nil, nil, nil)

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
	c, rec := echotest.Post(t, "/v1/responses", &explodingReadCloser{})
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedResponsesReq)

	input, ok := provider.capturedResponsesReq.Input.([]core.ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1)
	require.NotNil(t, input[0].ExtraFields.Lookup("x_trace"))

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedResponsesRequest())
	require.Equal(t, provider.capturedResponsesReq, env.CachedResponsesRequest())
}

func TestEmbeddings_UsesIngressFrameForDecoding(t *testing.T) {
	provider := &capturingProvider{
		supportedModels: []string{"text-embedding-3-large"},
		embeddingResponse: &core.EmbeddingResponse{
			Object: "list",
			Model:  "text-embedding-3-large",
			Data: []core.EmbeddingData{
				{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2]`), Index: 0},
			},
		},
	}

	handler := NewHandler(provider, nil, nil, nil)

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
	c, rec := echotest.Post(t, "/v1/embeddings", &explodingReadCloser{})
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err := handler.Embeddings(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.capturedEmbeddingReq)
	require.NotNil(t, provider.capturedEmbeddingReq.ExtraFields.Lookup("x_meta"), "x_meta missing from ExtraFields: %+v", provider.capturedEmbeddingReq.ExtraFields)

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedEmbeddingRequest())
	require.Equal(t, provider.capturedEmbeddingReq, env.CachedEmbeddingRequest())
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

	handler := NewHandler(mock, nil, nil, nil)

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
	c, rec := echotest.Post(t, "/v1/batches", &explodingReadCloser{})
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, mock.capturedBatchReq)
	require.NotNil(t, mock.capturedBatchReq.ExtraFields.Lookup("x_top"), "x_top missing from ExtraFields: %+v", mock.capturedBatchReq.ExtraFields)
	require.Len(t, mock.capturedBatchReq.Requests, 1)
	require.NotNil(t, mock.capturedBatchReq.Requests[0].ExtraFields.Lookup("x_item_flag"), "x_item_flag missing from item ExtraFields: %+v", mock.capturedBatchReq.Requests[0].ExtraFields)

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedBatchRequest())
	require.Equal(t, mock.capturedBatchReq, env.CachedBatchRequest())
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

	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
		"endpoint":"/v1/chat/completions",
		"requests":[{"custom_id":"chat-1","method":"POST","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}}]
	}`
	createCtx, createRec := echotest.Post(t, "/v1/batches", createBody)
	require.NoError(t, handler.Batches(createCtx))

	created := echotest.Decode[core.BatchResponse](t, createRec)

	frame := core.NewRequestSnapshot(http.MethodGet, "/v1/batches/"+created.ID, map[string]string{"id": created.ID}, nil, nil, "", nil, false, "", nil)
	getCtx, getRec := echotest.Get(t, "/v1/batches/wrong-id", echotest.WithPath("/v1/batches/:id"), echotest.WithPathValue("id", "wrong-id"))
	getCtx.SetRequest(withRequestSnapshotAndPrompt(getCtx.Request(), frame))

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

	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
		"endpoint":"/v1/chat/completions",
		"requests":[{"custom_id":"chat-1","method":"POST","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}}]
	}`
	createCtx, _ := echotest.Post(t, "/v1/batches", createBody)
	require.NoError(t, handler.Batches(createCtx))

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
	listCtx, listRec := echotest.Get(t, "/v1/batches?limit=bad")
	listCtx.SetRequest(withRequestSnapshotAndPrompt(listCtx.Request(), frame))

	require.NoError(t, handler.ListBatches(listCtx))
	require.Equal(t, http.StatusOK, listRec.Code)

	listResp := echotest.Decode[core.BatchListResponse](t, listRec)
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

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "stream": true, "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	contentType := rec.Header().Get("Content-Type")
	assert.Equal(t, "text/event-stream", contentType)

	body := rec.Body.String()
	assert.Contains(t, body, "data:")
	assert.Contains(t, body, "[DONE]")
}

func TestHandleStreamingResponse_FlushesEachChunk(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := &flushCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
	c := echo.New().NewContext(req, rec)

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
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 4, rec.flushes)
	got := rec.Body.String()
	require.Equal(t, "data: {\"id\":\"1\"}\n\ndata: {\"id\":\"2\"}\n\ndata: [DONE]\n\n", got)
}

func TestFlushStream_ReturnsReadError(t *testing.T) {
	expectedErr := errors.New("stream read failed")
	stream := &erroringReadCloser{
		data: []byte("data: {\"id\":\"1\"}\n\n"),
		err:  expectedErr,
	}

	err := flushStream(io.Discard, stream)
	require.ErrorIs(t, err, expectedErr)
}

func TestFlushStream_ReturnsWriteError(t *testing.T) {
	expectedErr := errors.New("client write failed")
	stream := io.NopCloser(strings.NewReader("data: {\"id\":\"1\"}\n\n"))

	err := flushStream(&erroringWriter{err: expectedErr}, stream)
	require.ErrorIs(t, err, expectedErr)
}

func TestRequestIDFromContextOrHeader(t *testing.T) {
	t.Run("prefers context request id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Request-ID", "header-id")
		req = req.WithContext(core.WithRequestID(req.Context(), "context-id"))
		got := requestIDFromContextOrHeader(req)
		require.Equal(t, "context-id", got)
	})

	t.Run("falls back to header request id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Request-ID", "  header-id  ")
		got := requestIDFromContextOrHeader(req)
		require.Equal(t, "header-id", got)
	})

	t.Run("nil request returns empty", func(t *testing.T) {
		got := requestIDFromContextOrHeader(nil)
		require.Empty(t, got)
	})
}

func TestHandleStreamingResponse_RecordsStreamingError(t *testing.T) {
	expectedErr := errors.New("upstream stream failed")
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}

	handler := NewHandler(&mockProvider{}, logger, nil, nil)

	entry := &auditlog.LogEntry{
		ID:        "entry-1",
		Timestamp: time.Now(),
		RequestID: "req-stream-1",
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		Data:      &auditlog.LogData{},
	}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil,
		echotest.WithHeader("X-Request-ID", "req-stream-1"),
		echotest.WithValue(string(auditlog.LogEntryKey), entry))

	err := handler.translatedInference().handleStreamingResponse(c, nil, "gpt-4o-mini", "openai", "primary-openai", func() (io.ReadCloser, error) {
		return &erroringReadCloser{
			data: []byte("data: {\"id\":\"1\"}\n\n"),
			err:  expectedErr,
		}, nil
	})
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	logged := logger.entries[0]
	require.Equal(t, "stream_error", logged.ErrorType)
	require.NotNil(t, logged.Data)
	require.Equal(t, expectedErr.Error(), logged.Data.ErrorMessage)
}

func TestHandleStreamingResponse_ClientDisconnectBeforeUpstream(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	entry := &auditlog.LogEntry{
		ID:        "entry-cancel",
		Timestamp: time.Now(),
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		Data:      &auditlog.LogData{},
	}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithValue(string(auditlog.LogEntryKey), entry))
	ctx, cancel := context.WithCancel(c.Request().Context())
	cancel() // simulate client gone before streamFn returns
	c.SetRequest(c.Request().WithContext(ctx))

	err := handler.translatedInference().handleStreamingResponse(c, nil, "gpt-4o-mini", "openai", "primary-openai", func() (io.ReadCloser, error) {
		return nil, context.Canceled
	})
	require.NoError(t, err)
	require.True(t, entry.Stream)
	require.Equal(t, "client_disconnected", entry.ErrorType)
}

// At pre-flush dispatch time the only socket in play is the upstream
// provider connection, so EPIPE / ECONNRESET on the error from streamFn
// belong to the provider and must surface as upstream failures rather than
// be swallowed as client disconnects.
func TestHandleStreamingResponse_UpstreamResetIsNotClassifiedAsClientDisconnect(t *testing.T) {
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}

	handler := NewHandler(&mockProvider{}, logger, nil, nil)

	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "bare syscall.ECONNRESET", err: syscall.ECONNRESET},
		{name: "wrapped syscall.EPIPE", err: fmt.Errorf("dial upstream: %w", syscall.EPIPE)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := &auditlog.LogEntry{
				ID:        "entry-upstream-reset",
				Timestamp: time.Now(),
				Method:    http.MethodPost,
				Path:      "/v1/chat/completions",
				Data:      &auditlog.LogData{},
			}
			c, rec := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithValue(string(auditlog.LogEntryKey), entry))

			err := handler.translatedInference().handleStreamingResponse(c, nil, "gpt-4o-mini", "openai", "primary-openai", func() (io.ReadCloser, error) {
				return nil, tt.err
			})

			// handleStreamingResponse always swallows the error by writing a
			// JSON response via handleError; the gateway response must be the
			// upstream failure, not an empty 200.
			require.NoError(t, err)
			require.NotEqual(t, http.StatusOK, rec.Code, "upstream reset surfaced as 200 OK; want non-2xx, got body=%q", rec.Body.String())
			require.NotEqual(t, "client_disconnected", entry.ErrorType, "upstream reset misclassified as client_disconnected (err=%v)", tt.err)
			require.True(t, entry.Stream)
		})
	}
}

func TestRecordStreamingError_ClassifiesClientDisconnect(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name     string
		ctx      context.Context
		err      error
		wantType string
	}{
		{
			name:     "explicit context.Canceled",
			ctx:      context.Background(),
			err:      context.Canceled,
			wantType: "client_disconnected",
		},
		{
			name:     "wrapped context.Canceled",
			ctx:      context.Background(),
			err:      fmt.Errorf("upstream send failed: %w", context.Canceled),
			wantType: "client_disconnected",
		},
		{
			name:     "syscall.EPIPE",
			ctx:      context.Background(),
			err:      syscall.EPIPE,
			wantType: "client_disconnected",
		},
		{
			name:     "wrapped syscall.EPIPE",
			ctx:      context.Background(),
			err:      fmt.Errorf("write to client: %w", syscall.EPIPE),
			wantType: "client_disconnected",
		},
		{
			name:     "syscall.ECONNRESET",
			ctx:      context.Background(),
			err:      syscall.ECONNRESET,
			wantType: "client_disconnected",
		},
		{
			name:     "canceled ctx racing real upstream error stays stream_error",
			ctx:      canceledCtx,
			err:      errors.New("upstream malformed"),
			wantType: "stream_error",
		},
		{
			name:     "clean ctx and generic error",
			ctx:      context.Background(),
			err:      errors.New("upstream malformed"),
			wantType: "stream_error",
		},
		{
			// Exercises the err==nil branch of isClientDisconnect and the
			// matching nil-guard fallback in recordStreamingError. Must not
			// panic and must record the context error as the message.
			name:     "canceled ctx with nil err",
			ctx:      canceledCtx,
			err:      nil,
			wantType: "client_disconnected",
		},
		{
			name:     "stall deadline expiry",
			ctx:      context.Background(),
			err:      fmt.Errorf("%w for 1m0s: write tcp: i/o timeout", ErrClientStall),
			wantType: "client_stalled",
		},
		{
			// net/http cancels the request context once a write fails, so a
			// stall usually arrives with a canceled ctx; the stall still wins.
			name:     "stall deadline expiry with canceled ctx",
			ctx:      canceledCtx,
			err:      fmt.Errorf("%w for 1m0s: write tcp: i/o timeout", ErrClientStall),
			wantType: "client_stalled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
			recordStreamingError(entry, "gpt-4o-mini", "openai", "/v1/chat/completions", "req-"+tt.name, tt.ctx, tt.err)
			require.Equal(t, tt.wantType, entry.ErrorType)

			wantMessage := ""
			switch {
			case tt.err != nil:
				wantMessage = tt.err.Error()
			case tt.ctx != nil && tt.ctx.Err() != nil:
				wantMessage = tt.ctx.Err().Error()
			}
			require.Equal(t, wantMessage, entry.Data.ErrorMessage)
		})
	}
}

func TestChatCompletionStreaming_FlushesBeforeNextChunkArrives(t *testing.T) {
	secondChunkStarted := make(chan struct{})
	releaseSecondChunk := make(chan struct{})

	provider := &streamingProviderWithCustomReader{
		supportedModels: []string{"gpt-4o-mini"},
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
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

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

	require.NoError(t, result.err)

	firstChunk := string(result.buf[:result.n])
	require.Contains(t, firstChunk, `"id":"1"`)
}

func TestHealth(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	c, rec := echotest.Get(t, "/health")

	err := handler.Health(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ok")
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

	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/models")

	err := handler.ListModels(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `"object":"list"`)
	assert.Contains(t, body, "gpt-4o-mini")
	assert.Contains(t, body, "gpt-4-turbo")
}

func TestListModels_AnthropicDialect(t *testing.T) {
	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:       "gpt-4o-mini",
					Object:   "model",
					Created:  1721172741,
					OwnedBy:  "system",
					Metadata: &core.ModelMetadata{DisplayName: "GPT-4o mini"},
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

	handler := NewHandler(mock, nil, nil, nil)

	// The anthropic-version header marks an Anthropic SDK client; the shared
	// models route renders the Anthropic list shape for it.
	c, rec := echotest.Get(t, "/v1/models", echotest.WithHeader("anthropic-version", "2023-06-01"))
	err := handler.ListModels(c)
	require.NoError(t, err)

	body := echotest.Decode[struct {
		Data []struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
		HasMore bool    `json:"has_more"`
		FirstID *string `json:"first_id"`
		LastID  *string `json:"last_id"`
	}](t, rec)
	require.Len(t, body.Data, 2)

	first := body.Data[0]
	assert.Equal(t, "model", first.Type)
	assert.Equal(t, "gpt-4o-mini", first.ID)
	assert.Equal(t, "GPT-4o mini", first.DisplayName)
	assert.Equal(t, "2024-07-16T23:32:21Z", first.CreatedAt)

	// Models without metadata fall back to the ID as display name.
	assert.Equal(t, "gpt-4-turbo", body.Data[1].DisplayName)
	assert.False(t, body.HasMore)
	assert.NotNil(t, body.FirstID)
	assert.Equal(t, "gpt-4o-mini", *body.FirstID)
	assert.NotNil(t, body.LastID)
	assert.Equal(t, "gpt-4-turbo", *body.LastID)
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
	service, err := virtualmodels.NewService(newAliasesTestStore(
		redirectVM("smart", "gpt-4o", "openai", true),
	), catalog, true)
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

	handler := NewHandler(mock, nil, nil, nil)
	handler.exposedModelLister = service

	c, rec := echotest.Get(t, "/v1/models")

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
	service, err := virtualmodels.NewService(newAliasesTestStore(
		redirectVM("smart", "gpt-4o", "openai", true),
	), catalog, true)
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

	handler := NewHandler(mock, nil, nil, nil)
	handler.exposedModelLister = service
	handler.keepOnlyAliasesAtModelsEndpoint = true

	c, rec := echotest.Get(t, "/v1/models")

	err = handler.ListModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	resp := echotest.Decode[core.ModelsResponse](t, rec)
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

	handler := NewHandler(mock, nil, nil, nil)
	handler.modelAuthorizer = authorizer
	handler.exposedModelLister = staticExposedModelLister{
		models: []core.Model{
			{ID: "openai/gpt-5", Object: "model", OwnedBy: "openai"},
			{ID: "openai/gpt-4o-mini", Object: "model", OwnedBy: "openai"},
		},
	}

	c, rec := echotest.Get(t, "/v1/models")

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

	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/models")

	err := handler.ListModels(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "error")
}

// Tests for typed error handling

func TestHandleError_ProviderError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewProviderError("openai", http.StatusBadGateway, "upstream error", nil),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "provider_error")
	assert.Contains(t, body, "upstream error")
}

func TestHandleError_RateLimitError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewRateLimitError("openai", "rate limit exceeded"),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "rate_limit_error")
	assert.Contains(t, body, "rate limit exceeded")
}

func TestHandleError_InvalidRequestError(t *testing.T) {
	param := "model"
	code := "model_not_found"
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewInvalidRequestError("invalid parameters", nil).WithParam(param).WithCode(code),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	body := echotest.Decode[map[string]any](t, rec)

	errorBody, ok := body["error"].(map[string]any)
	require.True(t, ok, "error body = %#v, want object", body["error"])
	assert.Equal(t, "invalid_request_error", errorBody["type"])
	assert.Equal(t, "invalid parameters", errorBody["message"])
	assert.Equal(t, param, errorBody["param"])
	assert.Equal(t, code, errorBody["code"])
}

func TestHandleError_AuthenticationError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewAuthenticationError("openai", "invalid API key"),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "authentication_error")
	assert.Contains(t, body, "invalid API key")
}

func TestHandleError_NotFoundError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewNotFoundError("model not found"),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "not_found_error")
	assert.Contains(t, body, "model not found")
}

func TestHandleError_StreamingError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewRateLimitError("openai", "rate limit exceeded during streaming"),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "stream": true, "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "rate_limit_error")
}

func TestHandleError_UnexpectedErrorUsesOpenAISchema(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             errors.New("boom"),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	body := echotest.Decode[map[string]any](t, rec)

	errorBody, ok := body["error"].(map[string]any)
	require.True(t, ok, "error body = %#v, want object", body["error"])
	assert.Equal(t, "provider_error", errorBody["type"])
	assert.Equal(t, "an unexpected error occurred", errorBody["message"])
	value, ok := errorBody["param"]
	assert.True(t, ok)
	assert.Nil(t, value)
	value, ok = errorBody["code"]
	assert.True(t, ok)
	assert.Nil(t, value)
}

func TestChatCompletion_InvalidJSON(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{invalid json}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "invalid_request_error")
	assert.Contains(t, body, "invalid request body")
}

func TestChatCompletion_InvalidContentType(t *testing.T) {
	provider := &capturingProvider{
		supportedModels: []string{"gpt-4o-mini"},
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":123}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Nil(t, provider.capturedChatReq)

	body := rec.Body.String()
	require.Contains(t, body, "invalid request body")
	require.Contains(t, body, "string or array of content parts")
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

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "text-embedding-3-small", "input": "hello world"}`
	c, rec := echotest.Post(t, "/v1/embeddings", reqBody)

	err := handler.Embeddings(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "text-embedding-3-small")
	assert.Contains(t, body, "embedding")
}

func TestEmbeddings_InvalidJSON(t *testing.T) {
	mock := &mockProvider{supportedModels: []string{"text-embedding-3-small"}}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{bad json}`
	c, rec := echotest.Post(t, "/v1/embeddings", reqBody)

	err := handler.Embeddings(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmbeddings_ProviderReturnsError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingErr:    core.NewInvalidRequestError("embeddings not supported by this provider", nil),
	}

	handler := NewHandler(mock, nil, nil, nil)

	reqBody := `{"model": "text-embedding-3-small", "input": "hello"}`
	c, rec := echotest.Post(t, "/v1/embeddings", reqBody)

	err := handler.Embeddings(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "embeddings not supported")
}

// TestEmbeddings_ExactCache covers the exact cache on /v1/embeddings: an
// identical repeat is served from the cache, and anything the provider would
// answer differently for (input, model, dimensions, encoding_format) or a
// no-store request still reaches the provider.
func TestEmbeddings_ExactCache(t *testing.T) {
	const firstBody = `{"model":"text-embedding-3-small","input":"hello world","dimensions":256,"encoding_format":"float"}`

	tests := []struct {
		name       string
		secondBody string
		header     string
		wantHit    bool
	}{
		{name: "identical request hits", secondBody: firstBody, wantHit: true},
		{name: "reformatted request hits", secondBody: `{ "input":"hello world", "model":"text-embedding-3-small", "encoding_format":"float", "dimensions":256 }`, wantHit: true},
		{name: "different input misses", secondBody: `{"model":"text-embedding-3-small","input":"goodbye world","dimensions":256,"encoding_format":"float"}`},
		{name: "different model misses", secondBody: `{"model":"text-embedding-3-large","input":"hello world","dimensions":256,"encoding_format":"float"}`},
		{name: "different dimensions misses", secondBody: `{"model":"text-embedding-3-small","input":"hello world","dimensions":512,"encoding_format":"float"}`},
		{name: "different encoding_format misses", secondBody: `{"model":"text-embedding-3-small","input":"hello world","dimensions":256,"encoding_format":"base64"}`},
		{name: "different user misses", secondBody: `{"model":"text-embedding-3-small","input":"hello world","dimensions":256,"encoding_format":"float","user":"tenant-b"}`},
		{name: "no-store bypasses", secondBody: firstBody, header: "no-store"},
		{name: "no-cache bypasses", secondBody: firstBody, header: "no-cache"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockProvider{
				supportedModels: []string{"text-embedding-3-small", "text-embedding-3-large"},
				embeddingResponse: &core.EmbeddingResponse{
					Object: "list",
					Data: []core.EmbeddingData{
						{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2,0.3]`), Index: 0},
					},
					Model: "text-embedding-3-small",
					Usage: core.EmbeddingUsage{PromptTokens: 5, TotalTokens: 5},
				},
			}

			store := cache.NewMapStore()
			defer store.Close()
			mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
			defer mw.Close()

			handler := NewHandler(mock, nil, nil, nil)
			handler.responseCache = mw

			call := func(body, cacheControl string) *httptest.ResponseRecorder {
				t.Helper()
				var opts []echotest.Option
				if cacheControl != "" {
					opts = append(opts, echotest.WithHeader("Cache-Control", cacheControl))
				}
				c, rec := echotest.Post(t, "/v1/embeddings", body, opts...)
				err := handler.Embeddings(c)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				return rec
			}

			first := call(firstBody, "")
			got := first.Header().Get("X-Cache")
			require.Empty(t, got)
			err := // The cache write is asynchronous; drain it before the repeat.
				mw.Close()
			require.NoError(t, err)

			second := call(tt.secondBody, tt.header)
			gotHit := second.Header().Get("X-Cache") == "HIT (exact)"
			require.Equal(t, tt.wantHit, gotHit, "second request X-Cache = %q, want hit = %v", second.Header().Get("X-Cache"), tt.wantHit)

			wantCalls := 2
			if tt.wantHit {
				wantCalls = 1
				require.Equal(t, first.Body.String(), second.Body.String())
			}
			require.Equal(t, wantCalls, mock.embeddingCalls)
		})
	}
}

// TestEmbeddings_ProviderErrorNotCached ensures a failed embeddings request is
// not stored, so the retry still reaches the provider.
func TestEmbeddings_ProviderErrorNotCached(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingErr:    core.NewProviderError("openai", http.StatusInternalServerError, "boom", nil),
	}

	store := cache.NewMapStore()
	defer store.Close()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	defer mw.Close()

	handler := NewHandler(mock, nil, nil, nil)
	handler.responseCache = mw

	const body = `{"model":"text-embedding-3-small","input":"hello world"}`
	for i := range 2 {
		c, rec := echotest.Post(t, "/v1/embeddings", body)
		err := handler.Embeddings(c)
		require.NoError(t, err)
		require.NotEqual(t, http.StatusOK, rec.Code, "request %d: status = 200, want an error status", i+1)
		got := rec.Header().Get("X-Cache")
		require.Empty(t, got, "request %d: X-Cache = %q, want empty", i+1, got)
	}
	require.Equal(t, 2, mock.embeddingCalls)
}

func TestEmbeddings_WithUsageTracking(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingResponse: &core.EmbeddingResponse{
			Object: "list",
			Data: []core.EmbeddingData{
				{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2,0.3]`), Index: 0},
			},
			Model: "provider-canonical-embedding",
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

	handler := NewHandler(mock, nil, usageLog, resolver)

	reqBody := `{"model": "text-embedding-3-small", "input": "hello world"}`
	c, rec := echotest.Post(t, "/v1/embeddings", reqBody, echotest.WithHeader("X-Request-ID", "test-req-embed-usage"))

	err := handler.Embeddings(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedEntry)
	assert.Equal(t, 10, capturedEntry.InputTokens)
	assert.Equal(t, "test-req-embed-usage", capturedEntry.RequestID)
	assert.Equal(t, "text-embedding-3-small", resolver.model)
	assert.NotNil(t, capturedEntry.InputCost)
	assert.NotEqual(t, float64(0), *capturedEntry.InputCost)
}

func TestListModels_TypedError(t *testing.T) {
	mock := &mockProvider{
		err: core.NewProviderError("openai", http.StatusBadGateway, "failed to list models", nil),
	}

	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/models")

	err := handler.ListModels(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "provider_error")
	assert.Contains(t, body, "failed to list models")
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
	c, rec := echotest.Post(t, "/v1/batches", reqBody)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	resp := echotest.Decode[core.BatchResponse](t, rec)
	assert.Equal(t, "batch", resp.Object)
	assert.Equal(t, "in_progress", resp.Status)
	assert.Equal(t, "mock", resp.Provider)
	assert.Equal(t, "provider-batch-123", resp.ProviderBatchID)
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
	c, rec := echotest.Post(t, "/v1/batches", reqBody)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, mock.capturedBatchReq)

	resp := echotest.Decode[core.BatchResponse](t, rec)
	require.Equal(t, "openai", resp.Provider)
}

func TestBatches_UsesExplicitAliasResolverAndBatchPreparerWithoutAliasProviderDecorator(t *testing.T) {
	store := newAliasesTestStore(redirectVM("smart", "gpt-4o", "openai", true))
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
	service, err := virtualmodels.NewService(store, catalog, true)
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
	aliasBatchPreparer := virtualmodels.NewBatchPreparer(mock, service)

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
	c, rec := echotest.Post(t, "/v1/batches", reqBody)

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

	resp := echotest.Decode[core.BatchResponse](t, rec)
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

	handler := NewHandler(mock, nil, nil, nil)
	handler.batchRequestPreparer = preparer
	store := batchstore.NewMemoryStore()
	handler.SetBatchStore(store)

	c, rec := echotest.Post(t, "/v1/batches", `{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`)

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

	created := echotest.Decode[core.BatchResponse](t, rec)
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

func uploadBatchInputFileForTest(t *testing.T, handler *Handler, providerType string) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("purpose", "batch"))
	if providerType != "" {
		require.NoError(t, writer.WriteField("provider", providerType))
	}
	part, err := writer.CreateFormFile("file", "requests.jsonl")
	require.NoError(t, err)
	_, err = part.Write([]byte("{\"custom_id\":\"1\"}\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	uploadFrame := core.NewRequestSnapshot(http.MethodPost, "/v1/files", nil, nil, nil, writer.FormDataContentType(), nil, false, "", nil)
	uploadCtx, uploadRec := echotest.Post(t, "/v1/files", &body, echotest.WithContentType(writer.FormDataContentType()))
	uploadCtx.SetRequest(withRequestSnapshotAndPrompt(uploadCtx.Request(), uploadFrame))

	require.NoError(t, handler.CreateFile(uploadCtx))
	require.Equal(t, http.StatusOK, uploadRec.Code)
}

func createInputFileBatchForTest(t *testing.T, handler *Handler, inputFileID, metadataProvider string) *httptest.ResponseRecorder {
	t.Helper()

	payload := map[string]any{
		"input_file_id":     inputFileID,
		"endpoint":          "/v1/chat/completions",
		"completion_window": "24h",
	}
	if metadataProvider != "" {
		payload["metadata"] = map[string]string{"provider": metadataProvider}
	}
	batchCtx, batchRec := echotest.Post(t, "/v1/batches", payload)
	require.NoError(t, handler.Batches(batchCtx))
	return batchRec
}

func TestBatches_InputFileUsesStoredFileProviderWithoutMetadata(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o-mini":    "openai",
			"claude-3-haiku": "anthropic",
		},
		fileCreateResponse: &core.FileObject{
			ID:        "file_source",
			Object:    "file",
			Bytes:     32,
			CreatedAt: 1000,
			Filename:  "requests.jsonl",
			Purpose:   "batch",
			Provider:  "openai",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:          "provider-batch-1",
			Object:      "batch",
			Status:      "validating",
			CreatedAt:   1234567890,
			InputFileID: "file_source",
		},
	}

	fileStore := filestore.NewMemoryStore()
	handler := NewHandler(mock, nil, nil, nil)
	handler.SetFileStore(fileStore)

	uploadBatchInputFileForTest(t, handler, "openai")

	stored, err := fileStore.Get(context.Background(), "file_source")
	require.NoError(t, err)
	require.Equal(t, "openai", stored.ProviderType)

	batchRec := createInputFileBatchForTest(t, handler, "file_source", "")
	require.Equal(t, http.StatusOK, batchRec.Code)
	require.Equal(t, "openai", mock.capturedBatchProvider)
	require.NotNil(t, mock.capturedBatchReq)
	require.Equal(t, "file_source", mock.capturedBatchReq.InputFileID)
}

func TestBatches_InputFileUsesMetadataProviderOverride(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o-mini":    "openai",
			"claude-3-haiku": "anthropic",
		},
		fileCreateResponse: &core.FileObject{
			ID:        "file_source",
			Object:    "file",
			Bytes:     32,
			CreatedAt: 1000,
			Filename:  "requests.jsonl",
			Purpose:   "batch",
			Provider:  "openai",
		},
		batchCreateResponse: &core.BatchResponse{
			ID:          "provider-batch-1",
			Object:      "batch",
			Status:      "validating",
			CreatedAt:   1234567890,
			InputFileID: "file_source",
		},
	}

	fileStore := filestore.NewMemoryStore()
	handler := NewHandler(mock, nil, nil, nil)
	handler.SetFileStore(fileStore)

	uploadBatchInputFileForTest(t, handler, "openai")

	stored, err := fileStore.Get(context.Background(), "file_source")
	require.NoError(t, err)
	require.Equal(t, "openai", stored.ProviderType)

	batchRec := createInputFileBatchForTest(t, handler, "file_source", "anthropic")
	require.Equal(t, http.StatusOK, batchRec.Code)
	require.Equal(t, "anthropic", mock.capturedBatchProvider)
	require.NotNil(t, mock.capturedBatchReq)
	require.Equal(t, "file_source", mock.capturedBatchReq.InputFileID)
}

func TestBatches_LegacyFallbackUsesFileProvider(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o-mini":    "openai",
			"claude-3-haiku": "anthropic",
		},
		fileErrByProvider: map[string]error{
			"openai": core.NewNotFoundError("file not found"),
		},
		fileGetByProvider: map[string]*core.FileObject{
			"anthropic": {
				ID:        "file_source",
				Object:    "file",
				Bytes:     32,
				CreatedAt: 1000,
				Filename:  "requests.jsonl",
				Purpose:   "batch",
				Provider:  "anthropic",
			},
		},
		batchCreateResponse: &core.BatchResponse{
			ID:          "provider-batch-1",
			Object:      "batch",
			Status:      "validating",
			CreatedAt:   1234567890,
			InputFileID: "file_source",
		},
	}

	handler := NewHandler(mock, nil, nil, nil)
	handler.SetFileStore(emptyProviderFileStore{})

	batchRec := createInputFileBatchForTest(t, handler, "file_source", "")
	require.Equal(t, http.StatusOK, batchRec.Code)
	require.Equal(t, "anthropic", mock.capturedBatchProvider)
	require.NotNil(t, mock.capturedBatchReq)
	require.Equal(t, "file_source", mock.capturedBatchReq.InputFileID)
}

func TestBatches_FileStoreLookupErrorFallsBackToProvider(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o-mini":    "openai",
			"claude-3-haiku": "anthropic",
		},
		fileErrByProvider: map[string]error{
			"openai": core.NewNotFoundError("file not found"),
		},
		fileGetByProvider: map[string]*core.FileObject{
			"anthropic": {
				ID:        "file_source",
				Object:    "file",
				Bytes:     32,
				CreatedAt: 1000,
				Filename:  "requests.jsonl",
				Purpose:   "batch",
				Provider:  "anthropic",
			},
		},
		batchCreateResponse: &core.BatchResponse{
			ID:          "provider-batch-1",
			Object:      "batch",
			Status:      "validating",
			CreatedAt:   1234567890,
			InputFileID: "file_source",
		},
	}

	handler := NewHandler(mock, nil, nil, nil)
	handler.SetFileStore(failingFileStore{err: errors.New("file store unavailable")})

	batchRec := createInputFileBatchForTest(t, handler, "file_source", "")
	require.Equal(t, http.StatusOK, batchRec.Code)
	require.Equal(t, "anthropic", mock.capturedBatchProvider)
	require.NotNil(t, mock.capturedBatchReq)
	require.Equal(t, "file_source", mock.capturedBatchReq.InputFileID)
}

func TestBatches_FileStoreLookupErrorPreservesClientFallbackError(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini", "claude-3-haiku"},
		providerTypes: map[string]string{
			"gpt-4o-mini":    "openai",
			"claude-3-haiku": "anthropic",
		},
		fileErrByProvider: map[string]error{
			"anthropic": core.NewNotFoundError("file not found"),
			"openai":    core.NewInvalidRequestError("provider rejected file id", nil),
		},
	}

	handler := NewHandler(mock, nil, nil, nil)
	handler.SetFileStore(failingFileStore{err: errors.New("file store unavailable")})

	batchRec := createInputFileBatchForTest(t, handler, "file_source", "")
	require.Equal(t, http.StatusBadRequest, batchRec.Code)
	require.Contains(t, batchRec.Body.String(), "provider rejected file id")
	require.Nil(t, mock.capturedBatchReq)
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

	handler := NewHandler(mock, nil, nil, nil)
	handler.batchRequestPreparer = preparer

	c, rec := echotest.Post(t, "/v1/batches", `{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "metadata":{"provider":"openai"}
	}`)

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
	c, rec := echotest.Post(t, "/v1/batches", reqBody)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestBatches_InputFileRewritesAliasesAndPersistsBatchPreparation(t *testing.T) {
	store := newAliasesTestStore(redirectVM("smart", "gpt-4o", "openai", true))
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
	service, err := virtualmodels.NewService(store, catalog, true)
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

	handler := NewHandler(inner, nil, nil, nil)
	handler.modelResolver = service
	handler.batchRequestPreparer = virtualmodels.NewBatchPreparer(inner, service)
	batchStore := batchstore.NewMemoryStore()
	handler.SetBatchStore(batchStore)

	c, rec := echotest.Post(t, "/v1/batches", `{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, inner.capturedBatchReq)
	require.Equal(t, "file_rewritten", inner.capturedBatchReq.InputFileID)
	require.Len(t, inner.capturedFileCreateReqs, 1)
	require.Contains(t, string(inner.capturedFileCreateReqs[0].Content), `"model":"gpt-4o"`)

	created := echotest.Decode[core.BatchResponse](t, rec)
	require.Equal(t, "file_source", created.InputFileID)

	stored, err := batchStore.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "file_source", stored.Batch.InputFileID)
	require.Equal(t, "file_source", stored.OriginalInputFileID)
	require.Equal(t, "file_rewritten", stored.RewrittenInputFileID)
}

func TestBatches_InputFileRejectsUnsupportedExplicitProviderSelector(t *testing.T) {
	store := newAliasesTestStore(redirectVM("smart", "claude-3-7-sonnet", "anthropic", true))
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
	service, err := virtualmodels.NewService(store, catalog, true)
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
	aliasBatchPreparer := virtualmodels.NewBatchPreparer(mock, service)

	handler := NewHandler(mock, nil, nil, nil)
	handler.modelResolver = service
	handler.batchRequestPreparer = aliasBatchPreparer

	c, rec := echotest.Post(t, "/v1/batches", `{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported model: openai/smart")
	require.Contains(t, rec.Body.String(), `"code":"model_not_found"`)
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

	c, rec := echotest.Post(t, "/v1/batches", `{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`)

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
	store := newAliasesTestStore(redirectVM("smart", "gpt-4o", "openai", false))
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
	service, err := virtualmodels.NewService(store, catalog, true)
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

	handler := NewHandler(inner, nil, nil, nil)
	handler.modelResolver = service
	handler.batchRequestPreparer = virtualmodels.NewBatchPreparer(inner, service)

	c, rec := echotest.Post(t, "/v1/batches", `{
	  "input_file_id":"file_source",
	  "endpoint":"/v1/chat/completions",
	  "completion_window":"24h",
	  "metadata":{"provider":"openai"}
	}`)

	err = handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported model: smart")
	require.Contains(t, rec.Body.String(), `"code":"model_not_found"`)
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

	c, rec := echotest.Get(t, "/v1/batches/batch_1", echotest.WithPath("/v1/batches/:id"), echotest.WithPathValue("id", "batch_1"))

	err := handler.GetBatch(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"file_hidden"}, provider.capturedFileDeleteIDs)

	resp := echotest.Decode[core.BatchResponse](t, rec)
	require.Equal(t, "file_source", resp.InputFileID)
	require.Equal(t, "completed", resp.Status)

	stored, err := store.Get(context.Background(), "batch_1")
	require.NoError(t, err)
	require.Empty(t, stored.RewrittenInputFileID)
	require.Equal(t, "file_source", stored.Batch.InputFileID)
}

func TestBatches_EmptyRequests(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	reqBody := `{"requests":[]}`
	c, rec := echotest.Post(t, "/v1/batches", reqBody)

	err := handler.Batches(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
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

	handler := NewHandler(mock, nil, nil, nil)

	// 1) Create
	createBody := `{
	  "endpoint":"/v1/chat/completions",
	  "requests":[{"custom_id":"life-1","method":"POST","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}}]
	}`
	createCtx, createRec := echotest.Post(t, "/v1/batches", createBody)
	err := handler.Batches(createCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, createRec.Code)

	created := echotest.Decode[core.BatchResponse](t, createRec)
	require.NotEmpty(t, created.ID)

	// 2) Get
	getCtx, getRec := echotest.Get(t, "/v1/batches/"+created.ID, echotest.WithPath("/v1/batches/:id"), echotest.WithPathValue("id", created.ID))
	err = handler.GetBatch(getCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getRec.Code)

	// 3) List
	listCtx, listRec := echotest.Get(t, "/v1/batches?limit=10")
	err = handler.ListBatches(listCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listRec.Code)

	listResp := echotest.Decode[core.BatchListResponse](t, listRec)
	require.NotEmpty(t, listResp.Data)

	// 4) Results
	resCtx, resRec := echotest.Get(t, "/v1/batches/"+created.ID+"/results", echotest.WithPath("/v1/batches/:id/results"), echotest.WithPathValue("id", created.ID))
	err = handler.BatchResults(resCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resRec.Code)

	resultsResp := echotest.Decode[core.BatchResultsResponse](t, resRec)
	require.Equal(t, created.ID, resultsResp.BatchID)
	require.Len(t, resultsResp.Data, 1)

	// 5) Cancel (completed batch stays completed)
	cancelCtx, cancelRec := echotest.Post(t, "/v1/batches/"+created.ID+"/cancel", nil, echotest.WithPath("/v1/batches/:id/cancel"), echotest.WithPathValue("id", created.ID))
	err = handler.CancelBatch(cancelCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cancelRec.Code)
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

	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
	  "endpoint":"/v1/responses",
	  "requests":[{"custom_id":"resp-1","method":"POST","body":{"model":"claude-sonnet-4-5-20250929","input":"hi"}}]
	}`
	createCtx, createRec := echotest.Post(t, "/v1/batches", createBody)
	createCtx.SetRequest(createCtx.Request().WithContext(context.WithValue(createCtx.Request().Context(), ctxKey("phase"), "create")))
	err := handler.Batches(createCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, createRec.Code)
	require.False(t, strings.Contains(createRec.Body.String(), "request_endpoint_by_custom_id"), "create response leaked internal hints: %s", createRec.Body.String())
	require.Equal(t, "anthropic", mock.capturedBatchProvider)
	got := core.GetRequestID(mock.capturedBatchCtx)
	require.NotEmpty(t, got)

	require.Equal(t, "create", mock.capturedBatchCtx.Value(ctxKey("phase")))
	require.Equal(t, "anthropic", mock.clearedBatchHintProvider)
	require.Equal(t, "provider-batch-1", mock.clearedBatchHintID)

	created := echotest.Decode[core.BatchResponse](t, createRec)

	resCtx, resRec := echotest.Get(t, "/v1/batches/"+created.ID+"/results", echotest.WithPath("/v1/batches/:id/results"), echotest.WithPathValue("id", created.ID))
	resCtx.SetRequest(resCtx.Request().WithContext(context.WithValue(resCtx.Request().Context(), ctxKey("phase"), "results")))
	err = handler.BatchResults(resCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resRec.Code)
	got = mock.capturedBatchHints["resp-1"]
	require.Equal(t, "/v1/responses", got)
	require.Equal(t, "anthropic", mock.capturedBatchHintsProvider)
	require.Equal(t, "provider-batch-1", mock.capturedBatchHintsBatchID)
	got = core.GetRequestID(mock.capturedBatchHintsCtx)
	require.NotEmpty(t, got)

	require.Equal(t, "results", mock.capturedBatchHintsCtx.Value(ctxKey("phase")))
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

	handler := NewHandler(mock, nil, nil, nil)

	createBody := `{
	  "endpoint":"/v1/chat/completions",
	  "requests":[{"custom_id":"pending-1","method":"POST","body":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"hi"}]}}]
	}`
	createCtx, createRec := echotest.Post(t, "/v1/batches", createBody)
	err := handler.Batches(createCtx)
	require.NoError(t, err)

	created := echotest.Decode[core.BatchResponse](t, createRec)

	resCtx, resRec := echotest.Get(t, "/v1/batches/"+created.ID+"/results", echotest.WithPath("/v1/batches/:id/results"), echotest.WithPathValue("id", created.ID))
	err = handler.BatchResults(resCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resRec.Code)
	require.Contains(t, resRec.Body.String(), "results are not ready yet")
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

	c, rec := echotest.Get(t, "/v1/batches/batch_1/results", echotest.WithPath("/v1/batches/:id/results"), echotest.WithPathValue("id", "batch_1"))

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

	handler := NewHandler(mock, nil, usageLog, resolver)

	createBody := `{
	  "endpoint":"/v1/chat/completions",
	  "requests":[{"custom_id":"usage-1","method":"POST","body":{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"hi"}]}}]
	}`
	createCtx, createRec := echotest.Post(t, "/v1/batches", createBody, echotest.WithHeader("X-Request-ID", "batch-usage-request-id"))
	err := handler.Batches(createCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, createRec.Code)

	created := echotest.Decode[core.BatchResponse](t, createRec)

	// First results call should log usage.
	resCtx1, resRec1 := echotest.Get(t, "/v1/batches/"+created.ID+"/results", echotest.WithPath("/v1/batches/:id/results"), echotest.WithPathValue("id", created.ID))
	err = handler.BatchResults(resCtx1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resRec1.Code)

	// Second results call should not duplicate usage writes.
	resCtx2, resRec2 := echotest.Get(t, "/v1/batches/"+created.ID+"/results", echotest.WithPath("/v1/batches/:id/results"), echotest.WithPathValue("id", created.ID))
	err = handler.BatchResults(resCtx2)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resRec2.Code)
	require.Len(t, usageLog.entries, 1)

	entry := usageLog.entries[0]
	assert.Equal(t, "batch-usage-request-id", entry.RequestID)
	assert.Equal(t, "/v1/batches", entry.Endpoint)
	assert.Equal(t, "msg_usage_1", entry.ProviderID)
	assert.Equal(t, 1000, entry.InputTokens)
	assert.Equal(t, 500, entry.OutputTokens)
	assert.Equal(t, 1500, entry.TotalTokens)
	require.NotNil(t, entry.TotalCost)
	require.Greater(t, *entry.TotalCost, float64(0))

	// 1000 * 1$/Mt + 500 * 2$/Mt = 0.001 + 0.001 = 0.002
	expectedTotalCost := 0.002
	delta := *entry.TotalCost - expectedTotalCost
	if delta < 0 {
		delta = -delta
	}
	assert.LessOrEqual(t, delta, 1e-9, "TotalCost = %.6f, want %.6f", *entry.TotalCost, expectedTotalCost)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, "usage-1", entry.RawData["batch_custom_id"])
}

func TestGetBatch_NotFound(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/batches/missing", echotest.WithPath("/v1/batches/:id"), echotest.WithPathValue("id", "missing"))
	err := handler.GetBatch(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
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
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	srv.handler.drainSnapshotWrites()

	getReq := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_store_1", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	got := echotest.Decode[core.ResponsesResponse](t, getRec)
	require.Equal(t, "resp_store_1", got.ID)
	require.Equal(t, "mock", got.Provider)

	itemsReq := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_store_1/input_items?order=asc", nil)
	itemsRec := httptest.NewRecorder()
	srv.ServeHTTP(itemsRec, itemsReq)
	require.Equal(t, http.StatusOK, itemsRec.Code, itemsRec.Body.String())

	items := echotest.Decode[core.ResponseInputItemListResponse](t, itemsRec)
	require.Equal(t, "list", items.Object)
	require.Len(t, items.Data, 1)
	require.False(t, items.HasMore)

	var first map[string]any
	require.NoError(t, json.Unmarshal(items.Data[0], &first))
	require.Equal(t, "message", first["type"])
	require.Equal(t, "user", first["role"])

	content, ok := first["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)

	text, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_text", text["type"])
	require.Equal(t, "hello", text["text"])
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
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	srv.handler.drainSnapshotWrites()

	stored, err := store.Get(context.Background(), "resp_provider_name_1")
	require.NoError(t, err)
	require.Equal(t, "openai", stored.Provider)
	require.Equal(t, "openai_primary", stored.ProviderName)
}

func TestResponsesLifecycle_StoreFalseSkipsLocalSnapshot(t *testing.T) {
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_store_false_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
		},
	}
	srv := New(provider, &Config{ResponseStore: store})

	createReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello","store":false}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	srv.handler.drainSnapshotWrites()
	_, err := store.Get(context.Background(), "resp_store_false_1")
	require.ErrorIs(t, err, responsestore.ErrNotFound)
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Equal(t, "resp_store_failure_1", resp.ID)

	srv.handler.drainSnapshotWrites()

	counter := observability.ResponseSnapshotStoreFailures.WithLabelValues("mock", "", "store")
	got := testutil.ToFloat64(counter)
	require.Equal(t, float64(1), got)
}

// blockingResponseStore delays Create until released, to prove the request
// path does not wait on snapshot persistence.
type blockingResponseStore struct {
	responsestore.Store
	release chan struct{}
	created chan string
}

func (s *blockingResponseStore) Create(ctx context.Context, r *responsestore.StoredResponse) error {
	<-s.release
	if err := s.Store.Create(ctx, r); err != nil {
		return err
	}
	s.created <- r.Response.ID
	return nil
}

func TestResponsesLifecycle_SnapshotWriteDoesNotBlockResponse(t *testing.T) {
	inner := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	store := &blockingResponseStore{
		Store:   inner,
		release: make(chan struct{}),
		created: make(chan string, 1),
	}
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_async_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
		},
	}
	srv := New(provider, &Config{ResponseStore: store})

	// Release the blocked store write on every exit path so a hung request
	// goroutine cannot outlive the test.
	releaseOnce := sync.OnceFunc(func() { close(store.release) })
	t.Cleanup(releaseOnce)

	// The store write is blocked, yet the request must complete.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		defer close(served)
		srv.ServeHTTP(rec, req)
	}()
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("request blocked on the snapshot write; snapshot persistence must be asynchronous")
	}
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, err := inner.Get(context.Background(), "resp_async_1")
	require.ErrorIs(t, err, responsestore.ErrNotFound)

	// Releasing the write and draining must leave the snapshot stored.
	releaseOnce()
	srv.handler.drainSnapshotWrites()
	id := <-store.created
	require.Equal(t, "resp_async_1", id)
	_, err = inner.Get(context.Background(), "resp_async_1")
	require.NoError(t, err)
}

func TestResponsesLifecycle_SnapshotWriteSkippedAfterDrain(t *testing.T) {
	observability.ResetMetrics()
	store := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes: map[string]string{
			"gpt-5-mini": "mock",
		},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_after_drain_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
		},
	}
	srv := New(provider, &Config{ResponseStore: store})
	srv.handler.drainSnapshotWrites()

	// A handler outliving the shutdown drain must still answer the client,
	// but its snapshot write is skipped and recorded as a store failure.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, err := store.Get(context.Background(), "resp_after_drain_1")
	require.ErrorIs(t, err, responsestore.ErrNotFound)

	counter := observability.ResponseSnapshotStoreFailures.WithLabelValues("mock", "", "store")
	got := testutil.ToFloat64(counter)
	require.Equal(t, float64(1), got)
}

func TestHandlerSetResponseStoreUpdatesCachedTranslatedInferenceService(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)
	first := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())
	second := responsestore.NewMemoryStore(responsestore.WithUnboundedRetention())

	handler.SetResponseStore(first)
	service := handler.translatedInference()
	require.Equal(t, first, service.currentResponseStore())

	handler.SetResponseStore(second)
	require.Equal(t, second, service.currentResponseStore())
}

func TestHandlerSetResponseStoreIsConcurrentSafe(t *testing.T) {
	handler := NewHandler(&mockProvider{}, nil, nil, nil)
	_ = handler.translatedInference()

	var wg sync.WaitGroup
	for range 20 {
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
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_cancel_1/cancel", nil)
	cancelRec := httptest.NewRecorder()
	srv.ServeHTTP(cancelRec, cancelReq)
	require.Equal(t, http.StatusNotImplemented, cancelRec.Code, cancelRec.Body.String())

	body := echotest.Decode[map[string]map[string]any](t, cancelRec)
	require.Equal(t, "unsupported_response_operation", body["error"]["code"])
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
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	srv.handler.drainSnapshotWrites()

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/responses/resp_delete_1", nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code, deleteRec.Body.String())

	deleted := echotest.Decode[core.ResponseDeleteResponse](t, deleteRec)
	require.Equal(t, "resp_delete_1", deleted.ID)
	require.True(t, deleted.Deleted)

	getReq := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_delete_1", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusNotImplemented, getRec.Code, getRec.Body.String())
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
	require.Equal(t, http.StatusOK, tokensRec.Code, tokensRec.Body.String())

	tokens := echotest.Decode[core.ResponseInputTokensResponse](t, tokensRec)
	require.Equal(t, 42, tokens.InputTokens)

	compactReq := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
	compactReq.Header.Set("Content-Type", "application/json")
	compactRec := httptest.NewRecorder()
	srv.ServeHTTP(compactRec, compactReq)
	require.Equal(t, http.StatusOK, compactRec.Code, compactRec.Body.String())

	compact := echotest.Decode[core.ResponseCompactResponse](t, compactRec)
	require.Equal(t, "cmp_42", compact.ID)
	require.Equal(t, "mock", compact.Provider)
	require.Len(t, provider.capturedResponseUtilityReqs, 2)

	wantUtility := []responseUtilityCall{
		{provider: "mock", operation: "CountResponseInputTokens"},
		{provider: "mock", operation: "CompactResponse"},
	}
	require.Equal(t, wantUtility, provider.capturedResponseUtility)
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
	envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
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
	pricing  *core.ModelPricing
	model    string
	provider string
}

func (m *mockPricingResolver) ResolvePricing(model, provider string) *core.ModelPricing {
	m.model = model
	m.provider = provider
	return m.pricing
}

// capturingProvider is a mockProvider that captures the request passed to StreamResponses/StreamChatCompletion.
type capturingProvider struct {
	mockProvider
	capturedChatCtx      context.Context
	capturedChatReq      *core.ChatRequest
	capturedResponsesReq *core.ResponsesRequest
	capturedEmbeddingReq *core.EmbeddingRequest
}

func (c *capturingProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	c.capturedChatCtx = ctx
	c.capturedChatReq = req
	if c.err != nil {
		return nil, c.err
	}
	return c.response, nil
}

func (c *capturingProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	c.capturedChatCtx = ctx
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
		supportedModels: []string{"gpt-4o-mini"},
		streamData:      streamData,
		providerName:    "gemini",
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	c, rec := echotest.Post(t, "/v1/responses", reqBody)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, provider.capturedResponsesReq)
	require.Nil(t, provider.capturedResponsesReq.StreamOptions)
	require.NotNil(t, provider.capturedChatReq)
	require.NotNil(t, provider.capturedChatReq.StreamOptions)
	require.True(t, provider.capturedChatReq.StreamOptions.IncludeUsage)
}

func TestStreamingResponses_ChatBackedProviderDoesNotInjectUsageWhenDisabled(t *testing.T) {
	provider := &chatBackedResponsesProvider{
		supportedModels: []string{"gpt-4o-mini"},
		streamData:      "data: [DONE]\n\n",
		providerName:    "gemini",
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: false,
		},
	}

	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	c, _ := echotest.Post(t, "/v1/responses", reqBody)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.NotNil(t, provider.capturedChatReq)
	require.Nil(t, provider.capturedChatReq.StreamOptions)
}

func TestStreamingResponses_NativeProviderRequestRemainsUnchanged(t *testing.T) {
	streamData := "data: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	provider := &capturingProvider{
		supportedModels: []string{"gpt-4o-mini"},
		streamData:      streamData,
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	c, rec := echotest.Post(t, "/v1/responses", reqBody)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, provider.capturedResponsesReq)
	require.Nil(t, provider.capturedResponsesReq.StreamOptions)
}

func TestStreamingResponses_ChatBackedProviderWritesExactlyOneUsageEntry(t *testing.T) {
	streamData := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"gemini-2.0-flash","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"chatcmpl-1","model":"gemini-2.0-flash","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	provider := &chatBackedResponsesProvider{
		supportedModels: []string{"gemini-2.0-flash"},
		streamData:      streamData,
		providerName:    "gemini",
	}
	usageLog := &collectingUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	handler := NewHandler(provider, nil, usageLog, nil)

	reqBody := `{"model":"gemini-2.0-flash","input":"Hello","stream":true}`
	c, _ := echotest.Post(t, "/v1/responses", reqBody, echotest.WithHeader("X-Request-ID", "req-stream-responses-1"))
	err := handler.Responses(c)
	require.NoError(t, err)
	require.Len(t, usageLog.entries, 1)

	entry := usageLog.entries[0]
	require.Equal(t, "req-stream-responses-1", entry.RequestID)
	require.Equal(t, "mock", entry.Provider)
	require.Equal(t, "/v1/responses", entry.Endpoint)
	require.Equal(t, 11, entry.InputTokens)
	require.Equal(t, 5, entry.OutputTokens)
	require.Equal(t, 16, entry.TotalTokens)
}

func TestResponses_PreservesUnknownNestedFields(t *testing.T) {
	provider := &capturingProvider{
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
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{
		"model":"gpt-5-mini",
		"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}]
	}`
	c, _ := echotest.Post(t, "/v1/responses", reqBody)
	err := handler.Responses(c)
	require.NoError(t, err)
	require.NotNil(t, provider.capturedResponsesReq)

	input, ok := provider.capturedResponsesReq.Input.([]core.ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1)
	require.NotNil(t, input[0].ExtraFields.Lookup("x_trace"))

	body, err := json.Marshal(provider.capturedResponsesReq)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	decodedInput := decoded["input"].([]any)
	firstInput := decodedInput[0].(map[string]any)
	_, ok = firstInput["x_trace"].(map[string]any)
	require.True(t, ok, "input[0].x_trace = %#v, want object", firstInput["x_trace"])
}

func TestStreamingChatCompletion_InjectsStreamOptions(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	provider := &capturingProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		streamData: streamData,
	}

	usageLog := &mockUsageLogger{
		config: usage.Config{
			Enabled:                   true,
			EnforceReturningUsageData: true,
		},
	}

	handler := NewHandler(provider, nil, usageLog, nil)

	// Streaming ChatCompletion request SHOULD have StreamOptions injected
	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/chat/completions", reqBody)

	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, provider.lastPassthroughReq)
	require.NotNil(t, provider.capturedChatReq.StreamOptions)
	assert.True(t, provider.capturedChatReq.StreamOptions.IncludeUsage)
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
	err := writer.WriteField("purpose", "batch")
	require.NoError(t, err)

	part, err := writer.CreateFormFile("file", "requests.jsonl")
	require.NoError(t, err)
	_, err = part.Write([]byte("{\"custom_id\":\"1\"}\n"))
	require.NoError(t, err)
	err = writer.Close()
	require.NoError(t, err)

	handler := NewHandler(mock, nil, nil, nil)

	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/files", nil, nil, nil, writer.FormDataContentType(), nil, false, "", nil)
	c, rec := echotest.Post(t, "/v1/files", &body, echotest.WithContentType(writer.FormDataContentType()))
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err = handler.CreateFile(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\"object\":\"file\"")

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedFileRouteInfo())
	require.Equal(t, "batch", env.CachedFileRouteInfo().Purpose)
	require.Equal(t, "requests.jsonl", env.CachedFileRouteInfo().Filename)
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
	handler := NewHandler(provider, nil, nil, nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	err := writer.WriteField("purpose", "batch")
	require.NoError(t, err)
	err = writer.WriteField("provider", "openai")
	require.NoError(t, err)

	part, err := writer.CreateFormFile("file", "requests.jsonl")
	require.NoError(t, err)
	_, err = part.Write([]byte("{\"custom_id\":\"1\"}\n"))
	require.NoError(t, err)
	err = writer.Close()
	require.NoError(t, err)

	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/files", nil, nil, nil, writer.FormDataContentType(), nil, false, "", nil)
	c, rec := echotest.Post(t, "/v1/files", &body, echotest.WithContentType(writer.FormDataContentType()))
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err = handler.CreateFile(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, base.capturedFileCreateReqs, 1)
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

	handler := NewHandler(mock, nil, nil, nil)

	// Get file
	getCtx, getRec := echotest.Get(t, "/v1/files/file_1", echotest.WithPath("/v1/files/:id"), echotest.WithPathValue("id", "file_1"))
	err := handler.GetFile(getCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getRec.Code)

	// Delete file
	delCtx, delRec := echotest.Request(t, http.MethodDelete, "/v1/files/file_1", nil, echotest.WithPath("/v1/files/:id"), echotest.WithPathValue("id", "file_1"))
	err = handler.DeleteFile(delCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, delRec.Code)

	// Get file content
	contentCtx, contentRec := echotest.Get(t, "/v1/files/file_1/content", echotest.WithPath("/v1/files/:id/content"), echotest.WithPathValue("id", "file_1"))
	err = handler.GetFileContent(contentCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, contentRec.Code)
	require.Contains(t, contentRec.Body.String(), "\"ok\":true")
}

func TestGetFileContent_TypedNilResponseReturnsBadGateway(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
		},
		providerNames: map[string]string{
			"gpt-4o-mini": "openai-primary",
		},
		fileContentByProv: map[string]*core.FileContentResponse{
			"openai": nil,
		},
	}

	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/files/file_1/content?provider=openai", echotest.WithPath("/v1/files/:id/content"), echotest.WithPathValue("id", "file_1"))
	err := handler.GetFileContent(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "provider_error")
	require.Contains(t, body, "provider openai-primary returned empty file content response")
	require.Contains(t, body, `"provider":"openai-primary"`)
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

	handler := NewHandler(mock, nil, nil, nil)

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
	c, rec := echotest.Get(t, "/v1/files?limit=5")
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err := handler.ListFiles(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\"object\":\"list\"")
	require.Contains(t, rec.Body.String(), "\"id\":\"file_ok_1\"")

	env := core.GetWhiteBoxPrompt(c.Request().Context())
	require.NotNil(t, env)
	require.NotNil(t, env.CachedFileRouteInfo())
	require.True(t, env.CachedFileRouteInfo().HasLimit)
	require.Equal(t, 5, env.CachedFileRouteInfo().Limit)
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

	handler := NewHandler(mock, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/files?after=missing-cursor")
	err := handler.ListFiles(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "after cursor file not found")
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

	handler := NewHandler(mock, nil, nil, nil)

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
	c, rec := echotest.Get(t, "/v1/files?limit=2&after=file_a1")
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	err := handler.ListFiles(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	resp := echotest.Decode[core.FileListResponse](t, rec)
	got := []string{resp.Data[0].ID, resp.Data[1].ID}
	require.Equal(t, []string{"file_o3", "file_o2"}, got)
	require.True(t, resp.HasMore)
	require.GreaterOrEqual(t, len(mock.fileListCalls), 3)

	lastCall := mock.fileListCalls[len(mock.fileListCalls)-1]
	require.Equal(t, "openai", lastCall.provider)
	require.Equal(t, "file_o4", lastCall.after)
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

	handler := NewHandler(mock, nil, nil, nil)

	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, rec := echotest.Get(t, "/v1/files/file_ok_1",
		echotest.WithPath("/v1/files/:id"),
		echotest.WithPathValue("id", "file_ok_1"),
		echotest.WithValue(string(auditlog.LogEntryKey), entry))
	err := handler.GetFile(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\"id\":\"file_ok_1\"")
	require.Equal(t, "openai", entry.Provider)
}

func TestGetFile_FileStoreLookupErrorFallsBackToProvider(t *testing.T) {
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"gpt-4o-mini": "openai",
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

	handler := NewHandler(mock, nil, nil, nil)
	handler.SetFileStore(failingFileStore{err: errors.New("file store unavailable")})

	c, rec := echotest.Get(t, "/v1/files/file_ok_1", echotest.WithPath("/v1/files/:id"), echotest.WithPathValue("id", "file_ok_1"))
	err := handler.GetFile(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\"provider\":\"openai\"")
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

	service, err := virtualmodels.NewService(newAliasesTestStore(
		redirectVM("gpt-4o", "claude-3-haiku", "", true),
	), &catalog, true)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

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

	handler := NewHandler(mock, nil, nil, nil)
	handler.modelResolver = service

	c, rec := echotest.Get(t, "/v1/files/file_ok_1", echotest.WithPath("/v1/files/:id"), echotest.WithPathValue("id", "file_ok_1"))
	err = handler.GetFile(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\"provider\":\"openai\"")
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
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Get(t, "/v1/files/file_ok_1", echotest.WithPath("/v1/files/:id"), echotest.WithPathValue("id", "file_ok_1"))
	err := handler.GetFile(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "provider_error")
	require.Contains(t, body, "file provider inventory is unavailable")
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

	gateway.MergeStoredBatchFromUpstream(stored, upstream)

	require.Equal(t, "openai", stored.Batch.Metadata["provider"])
	require.Equal(t, "provider-batch-1", stored.Batch.Metadata["provider_batch_id"])
	require.Equal(t, "upstream-overwrite", stored.Batch.Metadata["existing"])
	require.Equal(t, "new-value", stored.Batch.Metadata["new_key"])
	require.Equal(t, "file_source", stored.Batch.InputFileID)
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

	gateway.MergeStoredBatchFromUpstream(stored, upstream)
	got := stored.Batch.Status
	require.Equal(t, "in_progress", got)
	got = stored.Batch.Endpoint
	require.Equal(t, "/v1/chat/completions", got)
	got = stored.Batch.InputFileID
	require.Equal(t, "file_source", got)
	got = stored.Batch.CompletionWindow
	require.Equal(t, "24h", got)

	require.Equal(t, core.BatchRequestCounts{Total: 10, Completed: 4, Failed: 1}, stored.Batch.RequestCounts)
	require.Equal(t, core.BatchUsageSummary{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, InputCost: &inputCost, TotalCost: &totalCost}, stored.Batch.Usage)
	require.Len(t, stored.Batch.Results, 1)
	require.Equal(t, "keep-me", stored.Batch.Results[0].CustomID)
	require.NotNil(t, stored.Batch.InProgressAt)
	require.Equal(t, inProgressAt, *stored.Batch.InProgressAt)
	require.NotNil(t, stored.Batch.CompletedAt)
	require.Equal(t, completedAt, *stored.Batch.CompletedAt)
	got = stored.Batch.Metadata["upstream_only"]
	require.Equal(t, "value", got)
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

	require.Equal(t, http.StatusAccepted, rec.Code)
	got := rec.Body.String()
	require.Equal(t, `{"ok":true}`, got)
	got = rec.Header().Get("X-Upstream")
	require.Equal(t, "openai", got)
	got = rec.Header().Get("Set-Cookie")
	require.Empty(t, got)
	got = rec.Header().Get("X-Upstream-Hop")
	require.Empty(t, got)
	require.Equal(t, "openai", provider.lastPassthroughProvider)
	require.NotNil(t, provider.lastPassthroughReq)
	got = provider.lastPassthroughReq.Endpoint
	require.Equal(t, "responses?api-version=2026-03-10", got)
	got = readPassthroughRequestBody(t, provider.lastPassthroughReq.Body)
	require.Equal(t, `{"foo":"bar"}`, got)
	got = provider.lastPassthroughReq.Headers.Get("Authorization")
	require.Empty(t, got)
	got = provider.lastPassthroughReq.Headers.Get("Cookie")
	require.Empty(t, got)
	got = provider.lastPassthroughReq.Headers.Get("Forwarded")
	require.Empty(t, got)
	got = provider.lastPassthroughReq.Headers.Get("X-Forwarded-For")
	require.Empty(t, got)
	got = provider.lastPassthroughReq.Headers.Get("X-Debug")
	require.Empty(t, got)
	got = provider.lastPassthroughReq.Headers.Get("OpenAI-Beta")
	require.Equal(t, "responses=v1", got)
	got = provider.lastPassthroughReq.Headers.Get("X-Request-ID")
	require.Equal(t, "req_123", got)
	got = provider.lastPassthroughReq.Headers.Get(core.UserPathHeader)
	require.Empty(t, got)
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, provider.lastPassthroughReq)
	got := provider.lastPassthroughReq.Headers.Get("X-Request-ID")
	require.Equal(t, "ctx_req_123", got)
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

	require.Equal(t, http.StatusNotFound, rec.Code)
	got := rec.Header().Get("X-Upstream")
	require.Empty(t, got)
	body := rec.Body.String()
	require.Contains(t, body, `"message":"upstream missing"`)
	require.Contains(t, body, `"error"`)
}

func TestProviderPassthrough_LLMDDroppedReasonOnNormalizedError(t *testing.T) {
	for _, path := range []string{
		"/p/llmd/tokenize",
		"/p/llmd/v1/chat/completions",
	} {
		t.Run(path, func(t *testing.T) {
			provider := &mockProvider{
				passthroughResponse: &core.PassthroughResponse{
					StatusCode: http.StatusTooManyRequests,
					Headers: http.Header{
						"Content-Type":          {"application/json"},
						llmdDroppedReasonHeader: {"rejected-saturated"},
						"X-Upstream":            {"must-not-leak"},
						"Set-Cookie":            {"session=secret"},
					},
					Body: io.NopCloser(strings.NewReader(`{"error":{"message":"request dropped","type":"rate_limit_error"}}`)),
				},
			}

			e := echo.New()
			handler := NewHandler(provider, nil, nil, nil)
			e.POST("/p/:provider/*", handler.ProviderPassthrough)

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			got := rec.Header().Get(llmdDroppedReasonHeader)
			require.Equal(t, "rejected-saturated", got, "%s = %q, want rejected-saturated", llmdDroppedReasonHeader, got)
			got = rec.Header().Get("X-Upstream")
			require.Empty(t, got)
			got = rec.Header().Get("Set-Cookie")
			require.Empty(t, got)
			body := rec.Body.String()
			require.Contains(t, body, `"message":"request dropped"`)
		})
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, provider.lastPassthroughReq)
	got := provider.lastPassthroughReq.Endpoint
	require.Equal(t, "chat/completions", got)
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, provider.lastPassthroughReq)
	got := provider.lastPassthroughReq.Endpoint
	require.Equal(t, "messages", got)
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
		providerTypes: map[string]string{"openai_test/gpt-5-mini": "openai"},
		providerNames: map[string]string{"openai_test/gpt-5-mini": "openai_test"},
	}

	handler := NewHandler(provider, nil, nil, nil)

	entry := &auditlog.LogEntry{}
	c, rec := echotest.Post(t, "/p/openai_test/v1/chat/completions", `{"model":"gpt-5-mini"}`, echotest.WithValue(string(auditlog.LogEntryKey), entry))
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai_test",
			RawEndpoint:        "chat/completions",
			NormalizedEndpoint: "chat/completions",
			Model:              "gpt-5-mini",
			AuditPath:          "/v1/chat/completions",
		},
	})))
	err := handler.ProviderPassthrough(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gpt-5-mini", entry.RequestedModel)
	require.Equal(t, "openai", entry.Provider)
	require.Equal(t, "openai_test", entry.ProviderName)
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

	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)
	c, rec := echotest.Post(t, "/p/openai_test/chat/completions", `{"model":"gpt-5-mini"}`)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai_test",
			RawEndpoint:        "chat/completions",
			NormalizedEndpoint: "chat/completions",
			Model:              "gpt-5-mini",
			AuditPath:          "/p/openai_test/chat/completions",
		},
	})))
	err := handler.ProviderPassthrough(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "openai", provider.lastPassthroughProvider)
	require.Equal(t, "openai_test", authorizer.lastSelector.Provider)
	require.Equal(t, "gpt-5-mini", authorizer.lastSelector.Model)
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

	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)
	c, rec := echotest.Post(t, "/p/openai/chat/completions", `{"model":"gpt-5-mini"}`)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider:           "openai",
			RawEndpoint:        "chat/completions",
			NormalizedEndpoint: "chat/completions",
			Model:              "gpt-5-mini",
			AuditPath:          "/p/openai/chat/completions",
		},
	})))
	err := handler.ProviderPassthrough(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "openai", provider.lastPassthroughProvider)
	require.Equal(t, "openai_test", authorizer.lastSelector.Provider)
	require.Equal(t, "gpt-5-mini", authorizer.lastSelector.Model)
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

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "v1 alias is disabled")
	require.Nil(t, provider.lastPassthroughReq)
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

	require.Equal(t, http.StatusOK, rec.Code)
	got := rec.Header().Get("Content-Type")
	require.Equal(t, "text/event-stream", got)
	require.NotZero(t, rec.flushes)
	got = rec.Body.String()
	require.Contains(t, got, "message_start")
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, body.closes)
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
		providerTypes: map[string]string{"openai_test/gpt-5-mini": "openai"},
		providerNames: map[string]string{"openai_test/gpt-5-mini": "openai_test"},
	}
	usageLog := &collectingUsageLogger{
		config: usage.Config{Enabled: true},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, usageLog, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	req := httptest.NewRequest(http.MethodPost, "/p/openai_test/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-pass-stream-usage")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, usageLog.entries, 1)

	entry := usageLog.entries[0]
	require.Equal(t, "openai", entry.Provider)
	require.Equal(t, "openai_test", entry.ProviderName)
	require.Equal(t, "/p/openai_test/responses", entry.Endpoint)
	require.Equal(t, "gpt-5-mini", entry.Model)
	require.Equal(t, 10, entry.TotalTokens)
	require.Equal(t, "req-pass-stream-usage", entry.RequestID)
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, usageLog.entries, 1)
	got := usageLog.entries[0].Endpoint
	require.Equal(t, "/p/openai/v1/responses", got)
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
			got := passthroughStreamAuditPath(tt.requestPath, tt.provider, tt.endpoint)
			require.Equal(t, tt.want, got, "passthroughStreamAuditPath(%q, %q, %q) = %q, want %q", tt.requestPath, tt.provider, tt.endpoint, got, tt.want)
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

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `provider passthrough for \"groq\" is not enabled`)
	require.Contains(t, rec.Body.String(), "anthropic, deepseek, hetzner, kilo, llamacpp, llmd, openai, openrouter, sglang, vllm, zai")
}

func TestProviderPassthrough_ChutesRequiresExplicitOptIn(t *testing.T) {
	provider := &mockProvider{
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)

	blockedReq := httptest.NewRequest(http.MethodPost, "/p/chutes/provider-native/admin/keys", strings.NewReader(`{}`))
	blockedRec := httptest.NewRecorder()
	e.ServeHTTP(blockedRec, blockedReq)

	require.Equal(t, http.StatusBadRequest, blockedRec.Code, blockedRec.Body.String())
	require.Nil(t, provider.lastPassthroughReq)

	handler.setEnabledPassthroughProviders([]string{"chutes"})
	req := httptest.NewRequest(http.MethodPost, "/p/chutes/chat/completions", strings.NewReader(`{"model":"Qwen/Qwen3-32B-TEE"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "opt-in status = %d, want 200: %s", rec.Code, rec.Body.String())
	require.Equal(t, "chutes", provider.lastPassthroughProvider)
	require.NotNil(t, provider.lastPassthroughReq)
	require.Equal(t, "chat/completions", provider.lastPassthroughReq.Endpoint)
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

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "groq", provider.lastPassthroughProvider)
	require.NotNil(t, provider.lastPassthroughReq)
	got := provider.lastPassthroughReq.Endpoint
	require.Equal(t, "chat/completions", got)
	got = readPassthroughRequestBody(t, provider.lastPassthroughReq.Body)
	require.Equal(t, `{}`, got)
	got = rec.Body.String()
	require.Contains(t, got, `"ok":true`, "unexpected error body: %s", rec.Body.String())
}

func TestIsNativeBatchResultsPending(t *testing.T) {
	provider := &mockProvider{
		batchGetResponse: &core.BatchResponse{ID: "provider-batch-1", Status: "in_progress"},
	}
	anthropicErr := core.NewProviderError("anthropic", http.StatusNotFound, "pending", nil)
	pending, latest := gateway.IsNativeBatchResultsPending(context.Background(), provider, "anthropic", "provider-batch-1", anthropicErr)
	require.True(t, pending)
	require.NotNil(t, latest)
	require.Equal(t, "in_progress", latest.Status)

	openAIErr := core.NewProviderError("openai", http.StatusNotFound, "not found", nil)
	pending, _ = gateway.IsNativeBatchResultsPending(context.Background(), provider, "openai", "provider-batch-1", openAIErr)
	require.False(t, pending)

	provider.batchGetResponse = &core.BatchResponse{ID: "provider-batch-1", Status: "expired"}
	pending, _ = gateway.IsNativeBatchResultsPending(context.Background(), provider, "anthropic", "provider-batch-1", anthropicErr)
	require.False(t, pending)
}

// staticChainsResolver returns fixed plugin chains regardless of context,
// letting tests drive the production WorkflowRequestPatcher /
// WorkflowBatchPreparer and the response/stream phases with explicit chains.
type staticChainsResolver struct{ chains *plugins.Chains }

func (s staticChainsResolver) ChainsForContext(context.Context) *plugins.Chains {
	return s.chains
}

// A caller that carries only the user-path header (no managed key) must get
// the same policy-filtered model list that inference enforces for that path.
func TestListModels_ScopesByUserPathHeader(t *testing.T) {
	mock := &mockProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
				{ID: "anthropic/claude-sonnet-4-6", Object: "model", OwnedBy: "anthropic"},
			},
		},
	}
	authorizer := &userPathModelAuthorizer{allowedUnder: map[string]string{
		"/acme/eng": "anthropic",
	}}

	handler := NewHandler(mock, nil, nil, nil)
	handler.modelAuthorizer = authorizer

	list := func(headers map[string]string) (int, string) {
		var opts []echotest.Option
		for k, v := range headers {
			opts = append(opts, echotest.WithHeader(k, v))
		}
		c, rec := echotest.Get(t, "/v1/models", opts...)
		require.NoError(t, handler.ListModels(c))
		return rec.Code, rec.Body.String()
	}

	code, body := list(nil)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"id":"openai/gpt-4o"`)
	require.Contains(t, body, `"id":"anthropic/claude-sonnet-4-6"`)

	code, body = list(map[string]string{core.UserPathHeader: "/acme/eng/alice"})
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, `"id":"openai/gpt-4o"`)
	require.Contains(t, body, `"id":"anthropic/claude-sonnet-4-6"`)
	require.Equal(t, "/acme/eng/alice", authorizer.lastUserPath)

	code, body = list(map[string]string{core.UserPathHeader: "/acme/../eng"})
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body, "invalid "+core.UserPathHeader+" header")
	require.NotContains(t, body, `"object":"list"`)
}

// userPathModelAuthorizer allows a provider only under a user-path prefix and
// records the user path it was consulted with.
type userPathModelAuthorizer struct {
	allowedUnder map[string]string
	lastUserPath string
}

func (a *userPathModelAuthorizer) ValidateModelAccess(ctx context.Context, selector core.ModelSelector) error {
	if !a.AllowsModel(ctx, selector) {
		return core.NewInvalidRequestError("denied", nil)
	}
	return nil
}

func (a *userPathModelAuthorizer) AllowsModel(ctx context.Context, selector core.ModelSelector) bool {
	a.lastUserPath = core.UserPathFromContext(ctx)
	for prefix, provider := range a.allowedUnder {
		if strings.HasPrefix(a.lastUserPath, prefix) {
			return selector.Provider == provider
		}
	}
	return true
}

func (a *userPathModelAuthorizer) FilterPublicModels(ctx context.Context, models []core.Model) []core.Model {
	out := make([]core.Model, 0, len(models))
	for _, m := range models {
		selector, err := core.ParseModelSelector(m.ID, "")
		if err == nil && a.AllowsModel(ctx, selector) {
			out = append(out, m)
		}
	}
	return out
}

// stalledCachedClientWriter fails every body write the way the stall
// deadline writer does once the client stops reading.
type stalledCachedClientWriter struct {
	http.ResponseWriter
}

func (w *stalledCachedClientWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("%w for 3s: write tcp: i/o timeout", ErrClientStall)
}

func (w *stalledCachedClientWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// TestHandleWithCache_ClassifiesStalledClientOnCachedStream records a client
// that stalled while a cached stream was replayed: the audit entry carries
// error_type client_stalled instead of a clean cache hit, matching the live
// stream path.
func TestHandleWithCache_ClassifiesStalledClientOnCachedStream(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	s := &translatedInferenceService{responseCache: mw}

	req := &core.ChatRequest{Model: "gpt-4o-mini", Stream: true, Messages: []core.Message{{Role: "user", Content: "cached-stall"}}}
	body, err := marshalRequestBody(req)
	require.NoError(t, err)

	e := echo.New()
	newContext := func(w http.ResponseWriter) *echo.Context {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return e.NewContext(r, w)
	}

	primeCtx := newContext(httptest.NewRecorder())
	err = mw.HandleRequest(primeCtx, body, func() error {
		primeCtx.Response().Header().Set("Content-Type", "text/event-stream")
		primeCtx.Response().WriteHeader(http.StatusOK)
		_, _ = primeCtx.Response().Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"streamed\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1,\"total_tokens\":10}}\n\n" +
			"data: [DONE]\n\n"))
		return nil
	})
	require.NoError(t, err)
	err = mw.Close()
	require.NoError(t, err)

	c := newContext(&stalledCachedClientWriter{ResponseWriter: httptest.NewRecorder()})
	entry := &auditlog.LogEntry{ID: "audit-entry"}
	c.Set(string(auditlog.LogEntryKey), entry)
	err = handleWithCache(s, c, req, nil, func(*echo.Context, *core.ChatRequest, *core.Workflow) error {
		t.Fatal("cached stream must not dispatch to the provider")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "client_stalled", entry.ErrorType)
	require.NotNil(t, entry.Data)
	require.Contains(t, entry.Data.ErrorMessage, ErrClientStall.Error())
	require.Equal(t, auditlog.CacheTypeExact, entry.CacheType)
}

// guardrailChainWorkflow builds a resolved workflow whose policy carries the
// given guardrail chain identity (plugins.Chains.CacheHash).
func guardrailChainWorkflow(chainHash string) *core.Workflow {
	return &core.Workflow{
		Mode:         core.ExecutionModeTranslated,
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"},
		},
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID:      "v-" + chainHash,
			Features:       core.DefaultWorkflowFeatures(),
			GuardrailsHash: chainHash,
		},
	}
}

// cacheWriteSignalStore reports each completed cache write so a test can wait
// for the asynchronous store without shutting the middleware down.
type cacheWriteSignalStore struct {
	cache.Store
	writes chan struct{}
}

func newCacheWriteSignalStore() *cacheWriteSignalStore {
	return &cacheWriteSignalStore{Store: cache.NewMapStore(), writes: make(chan struct{}, 16)}
}

func (s *cacheWriteSignalStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	err := s.Store.Set(ctx, key, value, ttl)
	select {
	case s.writes <- struct{}{}:
	default:
	}
	return err
}

func (s *cacheWriteSignalStore) waitForWrite(t *testing.T) {
	t.Helper()
	select {
	case <-s.writes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the response cache write")
	}
}

// driveCachedChatRequest runs handleWithCache the way the translated service
// does, with the workflow's guardrail chain identity on the request context.
func driveCachedChatRequest(
	t *testing.T,
	s *translatedInferenceService,
	orchestrator *gateway.InferenceOrchestrator,
	workflow *core.Workflow,
	req *core.ChatRequest,
	body []byte,
	dispatch func(*echo.Context, *core.ChatRequest, *core.Workflow) error,
) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Post(t, "/v1/chat/completions", body)
	c.SetRequest(c.Request().WithContext(orchestrator.WithCacheRequestContext(c.Request().Context(), workflow)))
	err := handleWithCache(s, c, req, workflow, dispatch)
	require.NoError(t, err)

	return rec
}

// TestHandleWithCache_ExactEntryIsScopedToGuardrailChain covers the policy
// bypass where a cached body produced without a response guardrail was replayed
// to a request whose workflow declares one. Cache hits skip dispatch, where the
// response and stream chains run, so an entry may only serve a matching chain.
func TestHandleWithCache_ExactEntryIsScopedToGuardrailChain(t *testing.T) {
	store := newCacheWriteSignalStore()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	defer mw.Close()
	s := &translatedInferenceService{responseCache: mw}
	orchestrator := gateway.NewInferenceOrchestrator(gateway.InferenceConfig{})

	req := &core.ChatRequest{Model: "gpt-4o-mini", Messages: []core.Message{{Role: "user", Content: "give me the key"}}}
	body, err := marshalRequestBody(req)
	require.NoError(t, err)

	unguarded := guardrailChainWorkflow("")
	redacting := guardrailChainWorkflow("response-redaction-chain")

	dispatches := 0
	raw := func(c *echo.Context, _ *core.ChatRequest, _ *core.Workflow) error {
		dispatches++
		return c.JSON(http.StatusOK, map[string]string{"answer": "sk-ABCDEFGHIJKLMNOPQRSTUVWX1234"})
	}
	got := driveCachedChatRequest(t, s, orchestrator, unguarded, req, body, raw).Header().Get("X-Cache")
	require.Empty(t, got)

	store.waitForWrite(t)

	// Same chain: still a hit, and dispatch is skipped.
	hit := driveCachedChatRequest(t, s, orchestrator, unguarded, req, body, raw)
	got = hit.Header().Get("X-Cache")
	require.Equal(t, "HIT (exact)", got)
	require.Equal(t, 1, dispatches)

	// A workflow with a response-phase redaction step must not be served the
	// body stored under the unguarded chain.
	guarded := driveCachedChatRequest(t, s, orchestrator, redacting, req, body, func(c *echo.Context, _ *core.ChatRequest, _ *core.Workflow) error {
		dispatches++
		return c.JSON(http.StatusOK, map[string]string{"answer": "[redacted]"})
	})
	got = guarded.Header().Get("X-Cache")
	require.Empty(t, got)
	require.Equal(t, 2, dispatches)
	require.False(t, strings.Contains(guarded.Body.String(), "sk-ABCDEFGHIJKLMNOPQRSTUVWX1234"), "response guardrail was bypassed by the cache: %s", guarded.Body.String())

	// The guarded miss stores its own entry, which replays only to its chain.
	store.waitForWrite(t)
	replay := driveCachedChatRequest(t, s, orchestrator, redacting, req, body, raw)
	got = replay.Header().Get("X-Cache")
	require.Equal(t, "HIT (exact)", got)
	require.Contains(t, replay.Body.String(), "[redacted]")
	require.Equal(t, 2, dispatches)
}

// TestHandleWithCache_BlockedResponseIsNotServedFromCache covers a response
// guardrail that blocks: the blocked response is never stored, so a repeat
// request runs the chain again instead of replaying a would-be hit.
func TestHandleWithCache_BlockedResponseIsNotServedFromCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	defer mw.Close()
	s := &translatedInferenceService{responseCache: mw}
	orchestrator := gateway.NewInferenceOrchestrator(gateway.InferenceConfig{})

	req := &core.ChatRequest{Model: "gpt-4o-mini", Messages: []core.Message{{Role: "user", Content: "blocked"}}}
	body, err := marshalRequestBody(req)
	require.NoError(t, err)

	workflow := guardrailChainWorkflow("response-block-chain")

	dispatches := 0
	blocking := func(c *echo.Context, _ *core.ChatRequest, _ *core.Workflow) error {
		dispatches++
		return c.JSON(http.StatusUnavailableForLegalReasons, map[string]string{"error": "blocked by guardrail"})
	}

	for i := range 2 {
		rec := driveCachedChatRequest(t, s, orchestrator, workflow, req, body, blocking)
		got := rec.Header().Get("X-Cache")
		require.Empty(t, got, "request %d X-Cache = %q, want no cache hit for a blocked response", i+1, got)
		require.Equal(t, http.StatusUnavailableForLegalReasons, rec.Code, "request %d", i+1)
	}
	require.Equal(t, 2, dispatches)
}
