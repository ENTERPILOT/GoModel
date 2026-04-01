package aliases

import (
	"context"
	"strings"
	"testing"

	"gomodel/internal/core"
)

func TestBatchPreparerRewritesBatchInputFiles(t *testing.T) {
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

	preparer := NewBatchPreparer(inner, service)
	result, err := preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("PrepareBatchRequest() error = %v", err)
	}
	if result == nil || result.Request == nil {
		t.Fatal("PrepareBatchRequest() result missing request")
	}
	if result.Request.InputFileID != "file_rewritten" {
		t.Fatalf("rewritten input_file_id = %q, want file_rewritten", result.Request.InputFileID)
	}
	if result.OriginalInputFileID != "file_source" {
		t.Fatalf("OriginalInputFileID = %q, want file_source", result.OriginalInputFileID)
	}
	if result.RewrittenInputFileID != "file_rewritten" {
		t.Fatalf("RewrittenInputFileID = %q, want file_rewritten", result.RewrittenInputFileID)
	}
	if len(inner.fileCreates) != 1 {
		t.Fatalf("len(fileCreates) = %d, want 1", len(inner.fileCreates))
	}
	if got := string(inner.fileCreates[0].Content); !strings.Contains(got, "\"model\":\"gpt-4o\"") {
		t.Fatalf("rewritten file content = %s, want concrete model", got)
	}
}

func TestBatchPreparerRejectsAliasResolvedToDifferentProvider(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("anthropic/claude-3-7-sonnet", "anthropic", core.Model{ID: "claude-3-7-sonnet", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "claude-3-7-sonnet", TargetProvider: "anthropic", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["anthropic/claude-3-7-sonnet"] = true
	inner.providerType["anthropic/claude-3-7-sonnet"] = "anthropic"
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}

	preparer := NewBatchPreparer(inner, service)
	_, err = preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err == nil {
		t.Fatal("PrepareBatchRequest() error = nil, want provider mismatch")
	}
	if !strings.Contains(err.Error(), `native batch supports a single provider per batch`) {
		t.Fatalf("PrepareBatchRequest() error = %v, want mixed-provider validation error", err)
	}
	if len(inner.fileCreates) != 0 {
		t.Fatalf("len(fileCreates) = %d, want 0", len(inner.fileCreates))
	}
}

func TestBatchPreparerRejectsUnsupportedExplicitProviderSelector(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("anthropic/claude-3-7-sonnet", "anthropic", core.Model{ID: "claude-3-7-sonnet", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "claude-3-7-sonnet", TargetProvider: "anthropic", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	inner := newProviderMock()
	inner.supported["anthropic/claude-3-7-sonnet"] = true
	inner.providerType["anthropic/claude-3-7-sonnet"] = "anthropic"
	inner.fileContent = &core.FileContentResponse{
		ID:       "file_source",
		Filename: "batch.jsonl",
		Data:     []byte("{\"custom_id\":\"1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"smart\",\"provider\":\"openai\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}\n"),
	}

	preparer := NewBatchPreparer(inner, service)
	_, err = preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err == nil {
		t.Fatal("PrepareBatchRequest() error = nil, want unsupported model error")
	}
	if !strings.Contains(err.Error(), `unsupported model: openai/smart`) {
		t.Fatalf("PrepareBatchRequest() error = %v, want unsupported model: openai/smart", err)
	}
	if len(inner.fileCreates) != 0 {
		t.Fatalf("len(fileCreates) = %d, want 0", len(inner.fileCreates))
	}
}
