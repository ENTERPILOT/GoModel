package server

import (
	"context"
	"strings"

	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
)

// BatchRequestPreparer rewrites a native batch request before provider
// submission. This keeps batch-specific policy out of provider decorators.
type BatchRequestPreparer interface {
	PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error)
}

type batchRequestPreparerChain struct {
	fileTransport core.NativeFileRoutableProvider
	preparers     []BatchRequestPreparer
}

// ComposeBatchRequestPreparers runs explicit batch preparers in order and
// cleans up superseded rewritten input files between stages.
func ComposeBatchRequestPreparers(fileTransport core.NativeFileRoutableProvider, preparers ...BatchRequestPreparer) BatchRequestPreparer {
	filtered := make([]BatchRequestPreparer, 0, len(preparers))
	for _, preparer := range preparers {
		if preparer != nil {
			filtered = append(filtered, preparer)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &batchRequestPreparerChain{
		fileTransport: fileTransport,
		preparers:     filtered,
	}
}

func (c *batchRequestPreparerChain) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	current := req
	aggregate := &core.BatchRewriteResult{Request: req}
	activeRewrittenFileID := ""

	for _, preparer := range c.preparers {
		result, err := preparer.PrepareBatchRequest(ctx, providerType, current)
		if err != nil {
			batchrewrite.CleanupFile(ctx, c.fileTransport, providerType, activeRewrittenFileID, "failed to delete superseded batch input file")
			return nil, err
		}
		if result == nil {
			continue
		}
		if result.Request != nil {
			current = result.Request
		}
		if aggregate.OriginalInputFileID == "" {
			aggregate.OriginalInputFileID = strings.TrimSpace(result.OriginalInputFileID)
		}
		if rewritten := strings.TrimSpace(result.RewrittenInputFileID); rewritten != "" {
			if activeRewrittenFileID != "" && activeRewrittenFileID != rewritten {
				batchrewrite.CleanupFile(ctx, c.fileTransport, providerType, activeRewrittenFileID, "failed to delete superseded batch input file")
			}
			activeRewrittenFileID = rewritten
			aggregate.RewrittenInputFileID = rewritten
		}
		aggregate.RequestEndpointHints = batchrewrite.MergeEndpointHints(aggregate.RequestEndpointHints, result.RequestEndpointHints)
	}

	aggregate.Request = current
	return aggregate, nil
}
