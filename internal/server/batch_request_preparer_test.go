package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func TestComposeBatchRequestPreparers_CleansUpSupersededFilesAndMergesHints(t *testing.T) {
	fileRouter := &mockProvider{}
	first := &batchRequestPreparerStub{
		result: &core.BatchRewriteResult{
			Request: &core.BatchRequest{
				InputFileID: "file_alias",
				Endpoint:    "/v1/chat/completions",
			},
			RequestEndpointHints: map[string]string{
				"alias-1": "/v1/chat/completions",
			},
			OriginalInputFileID:  "file_source",
			RewrittenInputFileID: "file_alias",
		},
	}
	second := &batchRequestPreparerStub{
		result: &core.BatchRewriteResult{
			Request: &core.BatchRequest{
				InputFileID: "file_guarded",
				Endpoint:    "/v1/chat/completions",
			},
			RequestEndpointHints: map[string]string{
				"guardrail-1": "/v1/responses",
			},
			OriginalInputFileID:  "file_alias",
			RewrittenInputFileID: "file_guarded",
		},
	}

	preparer := ComposeBatchRequestPreparers(fileRouter, first, second)
	result, err := preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "file_guarded", result.Request.InputFileID)
	require.Equal(t, "file_source", result.OriginalInputFileID)
	require.Equal(t, "file_guarded", result.RewrittenInputFileID)
	require.Equal(t, map[string]string{
		"alias-1":     "/v1/chat/completions",
		"guardrail-1": "/v1/responses",
	}, result.RequestEndpointHints)
	require.Equal(t, []string{"file_alias"}, fileRouter.capturedFileDeleteIDs)
}

func TestComposeBatchRequestPreparers_CleansUpLatestRewrittenFileOnError(t *testing.T) {
	fileRouter := &mockProvider{}
	first := &batchRequestPreparerStub{
		result: &core.BatchRewriteResult{
			Request: &core.BatchRequest{
				InputFileID: "file_alias",
				Endpoint:    "/v1/chat/completions",
			},
			OriginalInputFileID:  "file_source",
			RewrittenInputFileID: "file_alias",
		},
	}
	second := &batchRequestPreparerStub{
		err: errors.New("boom"),
	}

	preparer := ComposeBatchRequestPreparers(fileRouter, first, second)
	result, err := preparer.PrepareBatchRequest(context.Background(), "openai", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	require.Nil(t, result)
	require.EqualError(t, err, "boom")
	require.Equal(t, []string{"file_alias"}, fileRouter.capturedFileDeleteIDs)
}
