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
