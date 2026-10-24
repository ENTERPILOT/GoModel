package batchrewrite

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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

func TestRecordResult(t *testing.T) {
	metadata := &core.BatchPreparationMetadata{}
	ctx := core.WithBatchPreparationMetadata(context.Background(), metadata)

	RecordResult(ctx, &core.BatchRewriteResult{
		OriginalInputFileID:  "file_original",
		RewrittenInputFileID: "file_rewritten",
	})

	require.Equal(t, "file_original", metadata.OriginalInputFileID)
	require.Equal(t, "file_rewritten", metadata.RewrittenInputFileID)
}

func TestCleanupFile(t *testing.T) {
	deleter := &recordingDeleter{}

	require.True(t, CleanupFile(context.Background(), deleter, "openai", " file_rewritten ", ""))

	want := []deleteCall{{providerType: "openai", fileID: "file_rewritten"}}
	require.Equal(t, want, deleter.calls)
}

func TestCleanupFileReturnsFalseOnDeleteError(t *testing.T) {
	deleter := &recordingDeleter{err: errors.New("delete failed")}

	require.False(t, CleanupFile(context.Background(), deleter, "openai", "file_rewritten", ""))
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
	require.Equal(t, want, merged)

	merged["a"] = "changed"
	require.Equal(t, "/v1/chat/completions", left["a"])
}

func TestMergeEndpointHintsEmpty(t *testing.T) {
	merged := MergeEndpointHints(nil, nil)
	require.Nil(t, merged)
}
