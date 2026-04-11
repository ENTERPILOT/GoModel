package batchrewrite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gomodel/internal/core"
)

type deleteCall struct {
	providerType string
	fileID       string
}

type recordingDeleter struct {
	calls []deleteCall
	err   error
}

func (d *recordingDeleter) DeleteFile(_ context.Context, providerType, id string) (*core.FileDeleteResponse, error) {
	d.calls = append(d.calls, deleteCall{providerType: providerType, fileID: id})
	if d.err != nil {
		return nil, d.err
	}
	return &core.FileDeleteResponse{ID: id, Deleted: true}, nil
}

func TestRecordPreparation(t *testing.T) {
	metadata := &core.BatchPreparationMetadata{}
	ctx := core.WithBatchPreparationMetadata(context.Background(), metadata)

	RecordPreparation(ctx, &core.BatchRequest{InputFileID: " file_original "}, &core.BatchRequest{InputFileID: " file_rewritten "})

	if metadata.OriginalInputFileID != "file_original" {
		t.Fatalf("OriginalInputFileID = %q, want file_original", metadata.OriginalInputFileID)
	}
	if metadata.RewrittenInputFileID != "file_rewritten" {
		t.Fatalf("RewrittenInputFileID = %q, want file_rewritten", metadata.RewrittenInputFileID)
	}
}

func TestRecordResult(t *testing.T) {
	metadata := &core.BatchPreparationMetadata{}
	ctx := core.WithBatchPreparationMetadata(context.Background(), metadata)

	RecordResult(ctx, &core.BatchRewriteResult{
		OriginalInputFileID:  "file_original",
		RewrittenInputFileID: "file_rewritten",
	})

	if metadata.OriginalInputFileID != "file_original" {
		t.Fatalf("OriginalInputFileID = %q, want file_original", metadata.OriginalInputFileID)
	}
	if metadata.RewrittenInputFileID != "file_rewritten" {
		t.Fatalf("RewrittenInputFileID = %q, want file_rewritten", metadata.RewrittenInputFileID)
	}
}

func TestCleanupFile(t *testing.T) {
	deleter := &recordingDeleter{}

	if !CleanupFile(context.Background(), deleter, "openai", " file_rewritten ", "") {
		t.Fatal("CleanupFile returned false, want true")
	}

	want := []deleteCall{{providerType: "openai", fileID: "file_rewritten"}}
	if !reflect.DeepEqual(deleter.calls, want) {
		t.Fatalf("calls = %#v, want %#v", deleter.calls, want)
	}
}

func TestCleanupFileReturnsFalseOnDeleteError(t *testing.T) {
	deleter := &recordingDeleter{err: errors.New("delete failed")}

	if CleanupFile(context.Background(), deleter, "openai", "file_rewritten", "") {
		t.Fatal("CleanupFile returned true, want false")
	}
}

func TestCleanupSupersededFile(t *testing.T) {
	metadata := &core.BatchPreparationMetadata{RewrittenInputFileID: "file_current"}
	ctx := core.WithBatchPreparationMetadata(context.Background(), metadata)
	deleter := &recordingDeleter{}

	if CleanupSupersededFile(ctx, deleter, "openai", "file_current", "") {
		t.Fatal("CleanupSupersededFile deleted current file")
	}
	if !CleanupSupersededFile(ctx, deleter, "openai", "file_old", "") {
		t.Fatal("CleanupSupersededFile returned false for superseded file")
	}

	want := []deleteCall{{providerType: "openai", fileID: "file_old"}}
	if !reflect.DeepEqual(deleter.calls, want) {
		t.Fatalf("calls = %#v, want %#v", deleter.calls, want)
	}
}

func TestMergeEndpointHints(t *testing.T) {
	left := map[string]string{"a": "/v1/chat/completions", "b": "/v1/responses"}
	right := map[string]string{"b": "/v1/chat/completions", "c": "/v1/embeddings"}

	merged := MergeEndpointHints(left, right)

	want := map[string]string{
		"a": "/v1/chat/completions",
		"b": "/v1/chat/completions",
		"c": "/v1/embeddings",
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %#v, want %#v", merged, want)
	}

	merged["a"] = "changed"
	if left["a"] != "/v1/chat/completions" {
		t.Fatal("MergeEndpointHints returned a map aliasing the left input")
	}
}

func TestMergeEndpointHintsEmpty(t *testing.T) {
	if merged := MergeEndpointHints(nil, nil); merged != nil {
		t.Fatalf("merged = %#v, want nil", merged)
	}
}
