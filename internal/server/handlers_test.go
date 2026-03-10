package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

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

	batchCreateResponse *core.BatchResponse
	batchGetResponse    *core.BatchResponse
	batchCancelResponse *core.BatchResponse
	batchResults        *core.BatchResultsResponse
	batchResultsErr     error
	batchErr            error

	fileCreateResponse  *core.FileObject
	fileGetResponse     *core.FileObject
	fileDeleteResponse  *core.FileDeleteResponse
	fileListResponse    *core.FileListResponse
	fileContentResponse *core.FileContentResponse
	fileErr             error
	fileListByProvider  map[string]*core.FileListResponse
	fileErrByProvider   map[string]error
	fileGetByProvider   map[string]*core.FileObject
	fileContentByProv   map[string]*core.FileContentResponse
}

func (m *mockProvider) Supports(model string) bool {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil {
		model = selector.Model
	}
	for _, supported := range m.supportedModels {
		if model == supported {
			return true
		}
	}
	return false
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
	}
	if m.Supports(model) {
		return "mock"
	}
	return ""
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

func (m *mockProvider) CreateBatch(_ context.Context, _ string, _ *core.BatchRequest) (*core.BatchResponse, error) {
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

func (m *mockProvider) CancelBatch(_ context.Context, _ string, _ string) (*core.BatchResponse, error) {
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

func (m *mockProvider) CreateFile(_ context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	if m.fileErr != nil {
		return nil, m.fileErr
	}
	if m.fileCreateResponse != nil {
		return m.fileCreateResponse, nil
	}
	return &core.FileObject{
		ID:        "file_mock_1",
		Object:    "file",
		Bytes:     int64(len(req.Content)),
		CreatedAt: 1000,
		Filename:  req.Filename,
		Purpose:   req.Purpose,
		Provider:  providerType,
	}, nil
}

func (m *mockProvider) ListFiles(_ context.Context, providerType, purpose string, _ int, _ string) (*core.FileListResponse, error) {
	if m.fileErrByProvider != nil {
		if err, ok := m.fileErrByProvider[providerType]; ok && err != nil {
			return nil, err
		}
	}
	if m.fileErr != nil {
		return nil, m.fileErr
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
	if provider.capturedChatReq.ExtraFields["response_format"] == nil {
		t.Fatalf("response_format missing from ExtraFields: %+v", provider.capturedChatReq.ExtraFields)
	}

	body, err := json.Marshal(provider.capturedChatReq)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !bytes.Contains(body, []byte(`"response_format"`)) {
		t.Fatalf("marshaled request missing response_format: %s", string(body))
	}
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

	err := handler.handleStreamingResponse(c, "gpt-4o-mini", "openai", func() (io.ReadCloser, error) {
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

	err := handler.handleStreamingResponse(c, "gpt-4o-mini", "openai", func() (io.ReadCloser, error) {
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
	mock := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		err:             core.NewInvalidRequestError("invalid parameters", nil),
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

	body := rec.Body.String()
	if !strings.Contains(body, "invalid_request_error") {
		t.Errorf("response should contain error type, got: %s", body)
	}
	if !strings.Contains(body, "invalid parameters") {
		t.Errorf("response should contain error message, got: %s", body)
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

func (c *capturingProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	c.capturedResponsesReq = req
	return io.NopCloser(strings.NewReader(c.streamData)), nil
}

func TestStreamingResponses_DoesNotInjectStreamOptions(t *testing.T) {
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

	// Streaming Responses request should NOT have StreamOptions injected
	reqBody := `{"model":"gpt-4o-mini","input":"Hello","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler.Responses(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if provider.capturedResponsesReq == nil {
		t.Fatalf("expected capturedResponsesReq to be set, got nil")
	}
	if provider.capturedResponsesReq.StreamOptions != nil {
		t.Errorf("Responses streaming should NOT have StreamOptions injected, got: %+v", provider.capturedResponsesReq.StreamOptions)
	}
}

func TestStreamingChatCompletion_InjectsStreamOptions(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
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

	if err := handler.GetFile(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"id\":\"file_ok_1\"") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
}

func TestMergeStoredBatchFromUpstreamPreservesGatewayMetadata(t *testing.T) {
	stored := &core.BatchResponse{
		ID:              "batch_1",
		Provider:        "openai",
		ProviderBatchID: "provider-batch-1",
		Metadata: map[string]string{
			"provider":          "openai",
			"provider_batch_id": "provider-batch-1",
			"existing":          "keep-me",
		},
	}
	upstream := &core.BatchResponse{
		Status: "completed",
		Metadata: map[string]string{
			"provider":          "anthropic",
			"provider_batch_id": "other-id",
			"existing":          "upstream-overwrite",
			"new_key":           "new-value",
		},
	}

	mergeStoredBatchFromUpstream(stored, upstream)

	if stored.Metadata["provider"] != "openai" {
		t.Fatalf("provider metadata overwritten: %q", stored.Metadata["provider"])
	}
	if stored.Metadata["provider_batch_id"] != "provider-batch-1" {
		t.Fatalf("provider_batch_id metadata overwritten: %q", stored.Metadata["provider_batch_id"])
	}
	if stored.Metadata["existing"] != "upstream-overwrite" {
		t.Fatalf("expected non-gateway key overwrite from upstream, got %q", stored.Metadata["existing"])
	}
	if stored.Metadata["new_key"] != "new-value" {
		t.Fatalf("expected merged upstream key, got %q", stored.Metadata["new_key"])
	}
}

func TestIsNativeBatchResultsPending(t *testing.T) {
	anthropicErr := core.NewProviderError("anthropic", http.StatusNotFound, "pending", nil)
	if !isNativeBatchResultsPending(anthropicErr) {
		t.Fatal("expected anthropic 404 to be treated as pending")
	}

	openAIErr := core.NewProviderError("openai", http.StatusNotFound, "not found", nil)
	if isNativeBatchResultsPending(openAIErr) {
		t.Fatal("expected openai 404 not to be treated as pending")
	}
}
