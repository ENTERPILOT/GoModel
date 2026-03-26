package aliases

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"gomodel/internal/core"
)

type providerMock struct {
	chatReq        *core.ChatRequest
	responsesReq   *core.ResponsesRequest
	embeddingReq   *core.EmbeddingRequest
	batchReq       *core.BatchRequest
	createBatchErr error
	fileContent    *core.FileContentResponse
	fileCreates    []*core.FileCreateRequest
	fileDeletes    []string
	fileObject     *core.FileObject
	modelsResp     *core.ModelsResponse
	supported      map[string]bool
	providerType   map[string]string
}

func newProviderMock() *providerMock {
	return &providerMock{
		supported:    map[string]bool{},
		providerType: map[string]string{},
		modelsResp:   &core.ModelsResponse{Object: "list", Data: []core.Model{}},
	}
}

func (m *providerMock) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	m.chatReq = req
	return &core.ChatResponse{Model: req.Model}, nil
}

func (m *providerMock) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	m.chatReq = req
	return io.NopCloser(nil), nil
}

func (m *providerMock) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return m.modelsResp, nil
}

func (m *providerMock) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	m.responsesReq = req
	return &core.ResponsesResponse{Model: req.Model}, nil
}

func (m *providerMock) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	m.responsesReq = req
	return io.NopCloser(nil), nil
}

func (m *providerMock) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	m.embeddingReq = req
	return &core.EmbeddingResponse{Model: req.Model}, nil
}

func (m *providerMock) Supports(model string) bool {
	return m.supported[model]
}

func (m *providerMock) GetProviderType(model string) string {
	return m.providerType[model]
}

func (m *providerMock) CreateBatch(_ context.Context, _ string, req *core.BatchRequest) (*core.BatchResponse, error) {
	m.batchReq = req
	if m.createBatchErr != nil {
		return nil, m.createBatchErr
	}
	return &core.BatchResponse{ID: "batch_1", Object: "batch"}, nil
}

func (m *providerMock) GetBatch(_ context.Context, _ string, _ string) (*core.BatchResponse, error) {
	return &core.BatchResponse{ID: "batch_1", Object: "batch"}, nil
}

func (m *providerMock) ListBatches(_ context.Context, _ string, _ int, _ string) (*core.BatchListResponse, error) {
	return &core.BatchListResponse{Object: "list"}, nil
}

func (m *providerMock) CancelBatch(_ context.Context, _ string, _ string) (*core.BatchResponse, error) {
	return &core.BatchResponse{ID: "batch_1", Object: "batch", Status: "cancelled"}, nil
}

func (m *providerMock) GetBatchResults(_ context.Context, _ string, _ string) (*core.BatchResultsResponse, error) {
	return &core.BatchResultsResponse{Object: "list", BatchID: "batch_1"}, nil
}

func (m *providerMock) CreateFile(_ context.Context, _ string, req *core.FileCreateRequest) (*core.FileObject, error) {
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
	m.fileCreates = append(m.fileCreates, &copy)
	if m.fileObject != nil {
		return m.fileObject, nil
	}
	return &core.FileObject{ID: "file_rewritten", Object: "file", Filename: req.Filename, Purpose: req.Purpose}, nil
}

func (m *providerMock) ListFiles(_ context.Context, _ string, _ string, _ int, _ string) (*core.FileListResponse, error) {
	return &core.FileListResponse{Object: "list"}, nil
}

func (m *providerMock) GetFile(_ context.Context, _ string, id string) (*core.FileObject, error) {
	return &core.FileObject{ID: id, Object: "file"}, nil
}

func (m *providerMock) DeleteFile(_ context.Context, _ string, id string) (*core.FileDeleteResponse, error) {
	m.fileDeletes = append(m.fileDeletes, id)
	return &core.FileDeleteResponse{ID: id, Object: "file", Deleted: true}, nil
}

func (m *providerMock) GetFileContent(_ context.Context, _ string, id string) (*core.FileContentResponse, error) {
	if m.fileContent != nil {
		return m.fileContent, nil
	}
	return &core.FileContentResponse{ID: id, Filename: "batch.jsonl", Data: []byte("{}\n")}, nil
}

func TestProviderResolvesRequestsAndExposesAliasModels(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model", OwnedBy: "openai"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["gpt-4o"] = true
	inner.providerType["gpt-4o"] = "openai"
	inner.modelsResp = &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-4o", Object: "model"}}}

	provider := NewProviderWithOptions(inner, service, Options{})

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "smart"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if inner.chatReq == nil || inner.chatReq.Model != "gpt-4o" {
		t.Fatalf("inner.chatReq = %#v, want rewritten model gpt-4o", inner.chatReq)
	}
	if !provider.Supports("smart") {
		t.Fatal("Supports(smart) = false, want true")
	}
	if got := provider.GetProviderType("smart"); got != "openai" {
		t.Fatalf("GetProviderType(smart) = %q, want openai", got)
	}
	selector, changed, err := provider.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if !changed || selector.QualifiedModel() != "gpt-4o" {
		t.Fatalf("ResolveModel() = (%q, %v), want gpt-4o,true", selector.QualifiedModel(), changed)
	}

	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models.Data) != 2 {
		t.Fatalf("len(ListModels().Data) = %d, want 2", len(models.Data))
	}
	if models.Data[0].ID != "gpt-4o" || models.Data[1].ID != "smart" {
		t.Fatalf("ListModels().Data = %#v, want concrete model plus alias", models.Data)
	}
}

func TestProviderMaskingAliasOverridesConcreteModelEntry(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model", OwnedBy: "openai"})
	catalog.add("gpt-4o-mini", "openai", core.Model{ID: "gpt-4o-mini", Object: "model", OwnedBy: "openai", Metadata: &core.ModelMetadata{DisplayName: "GPT-4o mini"}})

	service, err := NewService(newMemoryStore(Alias{Name: "gpt-4o", TargetModel: "gpt-4o-mini", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["gpt-4o"] = true
	inner.supported["gpt-4o-mini"] = true
	inner.providerType["gpt-4o"] = "openai"
	inner.providerType["gpt-4o-mini"] = "openai"
	inner.modelsResp = &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "gpt-4o", Object: "model", Metadata: &core.ModelMetadata{DisplayName: "GPT-4o"}},
		{ID: "gpt-4o-mini", Object: "model", Metadata: &core.ModelMetadata{DisplayName: "GPT-4o mini"}},
	}}

	provider := NewProviderWithOptions(inner, service, Options{})

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if inner.chatReq == nil || inner.chatReq.Model != "gpt-4o-mini" {
		t.Fatalf("masked chat request = %#v, want rewritten model gpt-4o-mini", inner.chatReq)
	}

	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models.Data) != 2 {
		t.Fatalf("len(ListModels().Data) = %d, want 2", len(models.Data))
	}
	if models.Data[0].ID != "gpt-4o" {
		t.Fatalf("first model id = %q, want gpt-4o", models.Data[0].ID)
	}
	if models.Data[0].Metadata == nil || models.Data[0].Metadata.DisplayName != "GPT-4o mini" {
		t.Fatalf("masked model metadata = %#v, want alias target metadata", models.Data[0].Metadata)
	}
}

func TestProviderDefaultsToInventoryAndBatchPreparationOnly(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model", OwnedBy: "openai"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["gpt-4o"] = true
	inner.providerType["gpt-4o"] = "openai"

	provider := NewProvider(inner, service)

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "smart"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if inner.chatReq == nil || inner.chatReq.Model != "smart" {
		t.Fatalf("inner.chatReq = %#v, want untranslated alias model smart", inner.chatReq)
	}
	if !provider.Supports("smart") {
		t.Fatal("Supports(smart) = false, want true")
	}
	if got := provider.GetProviderType("smart"); got != "openai" {
		t.Fatalf("GetProviderType(smart) = %q, want openai", got)
	}
}

func TestProviderCanDisableTranslatedRequestRewriting(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model", OwnedBy: "openai"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["gpt-4o"] = true
	inner.providerType["gpt-4o"] = "openai"
	inner.modelsResp = &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-4o", Object: "model"}}}

	provider := NewProviderWithOptions(inner, service, Options{
		DisableTranslatedRequestProcessing: true,
	})

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "smart"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if inner.chatReq == nil || inner.chatReq.Model != "smart" {
		t.Fatalf("inner.chatReq = %#v, want unchanged alias model smart", inner.chatReq)
	}
	if !provider.Supports("smart") {
		t.Fatal("Supports(smart) = false, want true")
	}
	if got := provider.GetProviderType("smart"); got != "openai" {
		t.Fatalf("GetProviderType(smart) = %q, want openai", got)
	}
	selector, changed, err := provider.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if !changed || selector.QualifiedModel() != "gpt-4o" {
		t.Fatalf("ResolveModel() = (%q, %v), want gpt-4o,true", selector.QualifiedModel(), changed)
	}
}

func TestProviderPrepareBatchRequestHonorsDisableNativeBatchPreparation(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("openai/gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["openai/gpt-4o"] = true
	inner.providerType["openai/gpt-4o"] = "openai"
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}

	provider := NewProviderWithOptions(inner, service, Options{
		DisableNativeBatchPreparation: true,
	})
	req := &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	}

	result, err := provider.PrepareBatchRequest(context.Background(), "openai", req)
	if err != nil {
		t.Fatalf("PrepareBatchRequest() error = %v", err)
	}
	if result == nil || result.Request != req {
		t.Fatalf("PrepareBatchRequest() result = %#v, want original request", result)
	}
	if len(inner.fileCreates) != 0 {
		t.Fatalf("len(fileCreates) = %d, want 0", len(inner.fileCreates))
	}
}

func TestProviderRewritesBatchItemBodies(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("openai/gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["openai/gpt-4o"] = true
	provider := NewProviderWithOptions(inner, service, Options{})

	body := json.RawMessage(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`)
	_, err = provider.CreateBatch(context.Background(), "openai", &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{{CustomID: "1", Body: body}},
	})
	if err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	if inner.batchReq == nil || len(inner.batchReq.Requests) != 1 {
		t.Fatalf("captured batch request = %#v, want one request", inner.batchReq)
	}

	var rewritten map[string]json.RawMessage
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &rewritten); err != nil {
		t.Fatalf("unmarshal rewritten batch item: %v", err)
	}
	if got := string(rewritten["model"]); got != `"gpt-4o"` {
		t.Fatalf("rewritten batch model = %s, want gpt-4o", got)
	}
	if _, exists := rewritten["provider"]; exists {
		t.Fatalf("rewritten batch item unexpectedly preserved provider hint: %s", inner.batchReq.Requests[0].Body)
	}
}

func TestProviderRewritesBatchInputFiles(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("openai/gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["openai/gpt-4o"] = true
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}
	inner.fileObject = &core.FileObject{ID: "file_rewritten", Object: "file", Filename: "batch.jsonl", Purpose: "batch"}
	provider := NewProviderWithOptions(inner, service, Options{})

	_, err = provider.CreateBatch(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	if inner.batchReq == nil {
		t.Fatal("captured batch request = nil")
	}
	if inner.batchReq.InputFileID != "file_rewritten" {
		t.Fatalf("rewritten input_file_id = %q, want file_rewritten", inner.batchReq.InputFileID)
	}
	if len(inner.fileCreates) != 1 {
		t.Fatalf("len(fileCreates) = %d, want 1", len(inner.fileCreates))
	}
	if got := string(inner.fileCreates[0].Content); !strings.Contains(got, "\"model\":\"gpt-4o\"") {
		t.Fatalf("rewritten file content = %s, want concrete model", got)
	}
}

func TestProviderBatchInputFileSkipsUploadWhenUnchanged(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("openai/gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["gpt-4o"] = true
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}
	provider := NewProviderWithOptions(inner, service, Options{})

	_, err = provider.CreateBatch(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	if inner.batchReq == nil {
		t.Fatal("captured batch request = nil")
	}
	if inner.batchReq.InputFileID != "file_source" {
		t.Fatalf("input_file_id = %q, want file_source", inner.batchReq.InputFileID)
	}
	if len(inner.fileCreates) != 0 {
		t.Fatalf("len(fileCreates) = %d, want 0", len(inner.fileCreates))
	}
}

func TestProviderRejectsDisabledAliasInBatchInputFiles(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("openai/gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: false}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["gpt-4o"] = true
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}
	provider := NewProviderWithOptions(inner, service, Options{})

	_, err = provider.CreateBatch(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err == nil {
		t.Fatal("CreateBatch() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "unsupported model: smart") {
		t.Fatalf("CreateBatch() error = %v, want unsupported model: smart", err)
	}
	if inner.batchReq != nil {
		t.Fatalf("captured batch request = %#v, want nil", inner.batchReq)
	}
	if len(inner.fileCreates) != 0 {
		t.Fatalf("len(fileCreates) = %d, want 0", len(inner.fileCreates))
	}
}

func TestProviderDeletesRewrittenBatchInputFileOnCreateFailure(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("openai/gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["openai/gpt-4o"] = true
	inner.createBatchErr = context.Canceled
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}
	inner.fileObject = &core.FileObject{ID: "file_rewritten", Object: "file", Filename: "batch.jsonl", Purpose: "batch"}
	provider := NewProviderWithOptions(inner, service, Options{})

	_, err = provider.CreateBatch(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err == nil {
		t.Fatal("CreateBatch() error = nil, want non-nil")
	}
	if len(inner.fileDeletes) != 1 || inner.fileDeletes[0] != "file_rewritten" {
		t.Fatalf("fileDeletes = %v, want [file_rewritten]", inner.fileDeletes)
	}
}
