package gateway

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
)

func TestMergeStoredBatchFromUpstreamPreservesGatewayOwnedMetadata(t *testing.T) {
	stored := &batchstore.StoredBatch{
		Batch: &core.BatchResponse{
			Metadata: map[string]string{
				"provider":          "openai",
				"provider_batch_id": "batch-primary",
				"client":            "original",
			},
		},
	}
	upstream := &core.BatchResponse{
		Metadata: map[string]string{
			"provider":          "anthropic",
			"provider_batch_id": "batch-upstream",
			"client":            "upstream",
		},
	}

	MergeStoredBatchFromUpstream(stored, upstream)
	got := stored.Batch.Metadata["provider"]
	require.Equal(t, "openai", got)
	got = stored.Batch.Metadata["provider_batch_id"]
	require.Equal(t, "batch-primary", got)
	got = stored.Batch.Metadata["client"]
	require.Equal(t, "upstream", got)
}

func TestDetermineBatchExecutionSelectionRejectsNilRequest(t *testing.T) {
	_, err := DetermineBatchExecutionSelectionWithAuthorizerAndInputFileResolver(context.Background(), nil, nil, nil, nil, nil)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, "batch request is required", gatewayErr.Message)
}

func TestExtractTokenTotalsOnlySynthesizesAuthoritativeTotals(t *testing.T) {
	input, output, total, hasUsage, hasTotal := extractTokenTotals(map[string]any{
		"input_tokens": 10,
	})
	require.Equal(t, 10, input)
	require.Equal(t, 0, output)
	require.Equal(t, 0, total)
	require.True(t, hasUsage)
	require.False(t, hasTotal)

	input, output, total, hasUsage, hasTotal = extractTokenTotals(map[string]any{
		"input_tokens":  10,
		"output_tokens": 5,
	})
	require.Equal(t, 10, input)
	require.Equal(t, 5, output)
	require.Equal(t, 15, total)
	require.True(t, hasUsage)
	require.True(t, hasTotal)
}

func TestIntFromFloat64RejectsBoundaryOverflow(t *testing.T) {
	outOfRange := float64(uint64(1) << (strconv.IntSize - 1))
	_, ok := intFromFloat64(outOfRange)
	require.False(t, ok)
	_, ok = intFromFloat64(1.9)
	require.False(t, ok)
}

func TestCloneRequestsForSelectorCopiesMutableFields(t *testing.T) {
	includeUsage := false
	chatReq := &core.ChatRequest{
		Model:         "alias",
		Provider:      "router",
		Messages:      []core.Message{{Role: "user", ToolCalls: []core.ToolCall{{ID: "call-1"}}}},
		Tools:         []map[string]any{{"type": "function"}},
		StreamOptions: &core.StreamOptions{IncludeUsage: includeUsage},
		Reasoning:     &core.Reasoning{Effort: "low"},
	}

	chatClone := CloneChatRequestForSelector(chatReq, core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"})
	chatClone.Messages[0].Role = "assistant"
	chatClone.Messages[0].ToolCalls[0].ID = "call-2"
	chatClone.Tools[0]["type"] = "changed"
	chatClone.StreamOptions.IncludeUsage = true
	chatClone.Reasoning.Effort = "high"

	require.Equal(t, "user", chatReq.Messages[0].Role)
	require.Equal(t, "call-1", chatReq.Messages[0].ToolCalls[0].ID, "chat messages were shared with clone: %#v", chatReq.Messages)
	got := chatReq.Tools[0]["type"]
	require.Equal(t, "function", got)
	require.False(t, chatReq.StreamOptions.IncludeUsage)
	require.Equal(t, "low", chatReq.Reasoning.Effort)

	responsesReq := &core.ResponsesRequest{
		Model:         "alias",
		Provider:      "router",
		Tools:         []map[string]any{{"type": "function"}},
		Metadata:      map[string]string{"client": "original"},
		StreamOptions: &core.StreamOptions{},
		Reasoning:     &core.Reasoning{Effort: "low"},
	}

	responsesClone := CloneResponsesRequestForSelector(responsesReq, core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"})
	responsesClone.Tools[0]["type"] = "changed"
	responsesClone.Metadata["client"] = "clone"
	responsesClone.StreamOptions.IncludeUsage = true
	responsesClone.Reasoning.Effort = "high"
	got = responsesReq.Tools[0]["type"]
	require.Equal(t, "function", got)

	require.Equal(t, "original", responsesReq.Metadata["client"])
	require.False(t, responsesReq.StreamOptions.IncludeUsage)
	require.Equal(t, "low", responsesReq.Reasoning.Effort)
}

func TestShouldEnforceReturningUsageDataRequiresEnabledLogger(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		UsageLogger: &usageCaptureLogger{
			config: usage.Config{
				Enabled:                   false,
				EnforceReturningUsageData: true,
			},
		},
	})

	require.False(t, orchestrator.ShouldEnforceReturningUsageData())
}

func TestStreamResponsesRejectsNilRequest(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	_, err := orchestrator.StreamResponses(context.Background(), nil, nil)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

func TestDispatchChatCompletionRejectsEmptyProviderResponse(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	_, _, err := orchestrator.DispatchChatCompletion(context.Background(), nil, &core.ChatRequest{Model: "gpt-4o-mini"})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	require.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
}

func TestStreamResponsesRejectsEmptyProviderStream(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	_, err := orchestrator.StreamResponses(context.Background(), nil, &core.ResponsesRequest{Model: "gpt-4o-mini"})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	require.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
}

func TestStreamResponsesFallsBackAfterEmptyPrimaryStream(t *testing.T) {
	provider := &streamFailoverProvider{
		streamsByModel: map[string]io.ReadCloser{
			"fallback": io.NopCloser(strings.NewReader("data: {}\n\n")),
		},
	}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider: provider,
		FailoverResolver: failoverResolverFunc(func(*core.RequestModelResolution, core.Operation) []core.ModelSelector {
			return []core.ModelSelector{{Provider: "openai", Model: "fallback"}}
		}),
	})
	workflow := &core.Workflow{
		Endpoint: core.DescribeEndpoint(http.MethodPost, "/v1/responses"),
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "primary"},
			ProviderType:     "openai",
		},
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-fallback",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: true,
				Failover:   true,
			},
		},
	}

	result, err := orchestrator.StreamResponses(context.Background(), workflow, &core.ResponsesRequest{Model: "primary"})
	require.NoError(t, err)

	defer result.Stream.Close()

	require.True(t, result.Meta.UsedFailover)
	require.Equal(t, "openai/fallback", result.Meta.FailoverModel)
	got := strings.Join(provider.responseStreamCalls, ",")
	require.Equal(t, "primary,fallback", got)
}

type failoverResolverFunc func(*core.RequestModelResolution, core.Operation) []core.ModelSelector

func (f failoverResolverFunc) ResolveFailovers(resolution *core.RequestModelResolution, op core.Operation) []core.ModelSelector {
	return f(resolution, op)
}

type streamFailoverProvider struct {
	streamsByModel      map[string]io.ReadCloser
	responseStreamCalls []string
}

func (p *streamFailoverProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *streamFailoverProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *streamFailoverProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *streamFailoverProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *streamFailoverProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	p.responseStreamCalls = append(p.responseStreamCalls, req.Model)
	return p.streamsByModel[req.Model], nil
}

func (p *streamFailoverProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func (p *streamFailoverProvider) Supports(string) bool { return true }

func (p *streamFailoverProvider) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil && selector.Provider != "" {
		return selector.Provider
	}
	return ""
}
