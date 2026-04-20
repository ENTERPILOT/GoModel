package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/usage"
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

	if got := stored.Batch.Metadata["provider"]; got != "openai" {
		t.Fatalf("provider metadata = %q, want openai", got)
	}
	if got := stored.Batch.Metadata["provider_batch_id"]; got != "batch-primary" {
		t.Fatalf("provider_batch_id metadata = %q, want batch-primary", got)
	}
	if got := stored.Batch.Metadata["client"]; got != "upstream" {
		t.Fatalf("client metadata = %q, want upstream", got)
	}
}

func TestDetermineBatchExecutionSelectionRejectsNilRequest(t *testing.T) {
	_, err := DetermineBatchExecutionSelectionWithAuthorizer(context.Background(), nil, nil, nil, nil)
	if err == nil {
		t.Fatal("DetermineBatchExecutionSelectionWithAuthorizer() error = nil, want error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest || gatewayErr.Message != "batch request is required" {
		t.Fatalf("gateway error = (%s, %q), want invalid batch request", gatewayErr.Type, gatewayErr.Message)
	}
}

func TestExtractTokenTotalsOnlySynthesizesAuthoritativeTotals(t *testing.T) {
	input, output, total, hasUsage, hasTotal := extractTokenTotals(map[string]any{
		"input_tokens": 10,
	})
	if input != 10 || output != 0 || total != 0 || !hasUsage || hasTotal {
		t.Fatalf("input-only totals = (%d,%d,%d,%t,%t), want (10,0,0,true,false)", input, output, total, hasUsage, hasTotal)
	}

	input, output, total, hasUsage, hasTotal = extractTokenTotals(map[string]any{
		"input_tokens":  10,
		"output_tokens": 5,
	})
	if input != 10 || output != 5 || total != 15 || !hasUsage || !hasTotal {
		t.Fatalf("complete totals = (%d,%d,%d,%t,%t), want (10,5,15,true,true)", input, output, total, hasUsage, hasTotal)
	}
}

func TestIntFromFloat64RejectsBoundaryOverflow(t *testing.T) {
	outOfRange := float64(uint64(1) << (strconv.IntSize - 1))
	if _, ok := intFromFloat64(outOfRange); ok {
		t.Fatalf("intFromFloat64(%g) ok = true, want false", outOfRange)
	}
	if _, ok := intFromFloat64(1.9); ok {
		t.Fatal("intFromFloat64(1.9) ok = true, want false")
	}
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

	if chatReq.Messages[0].Role != "user" || chatReq.Messages[0].ToolCalls[0].ID != "call-1" {
		t.Fatalf("chat messages were shared with clone: %#v", chatReq.Messages)
	}
	if got := chatReq.Tools[0]["type"]; got != "function" {
		t.Fatalf("chat tool type = %v, want function", got)
	}
	if chatReq.StreamOptions.IncludeUsage {
		t.Fatal("chat StreamOptions shared with clone")
	}
	if chatReq.Reasoning.Effort != "low" {
		t.Fatalf("chat Reasoning effort = %q, want low", chatReq.Reasoning.Effort)
	}

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

	if got := responsesReq.Tools[0]["type"]; got != "function" {
		t.Fatalf("responses tool type = %v, want function", got)
	}
	if got := responsesReq.Metadata["client"]; got != "original" {
		t.Fatalf("responses metadata = %q, want original", got)
	}
	if responsesReq.StreamOptions.IncludeUsage {
		t.Fatal("responses StreamOptions shared with clone")
	}
	if responsesReq.Reasoning.Effort != "low" {
		t.Fatalf("responses Reasoning effort = %q, want low", responsesReq.Reasoning.Effort)
	}
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

	if orchestrator.ShouldEnforceReturningUsageData() {
		t.Fatal("ShouldEnforceReturningUsageData() = true, want false when usage logging is disabled")
	}
}

func TestStreamResponsesRejectsNilRequest(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	_, err := orchestrator.StreamResponses(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamResponses() error = nil, want invalid request error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("gateway error type = %q, want invalid_request_error", gatewayErr.Type)
	}
}

func TestDispatchChatCompletionRejectsEmptyProviderResponse(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	_, _, _, _, _, err := orchestrator.DispatchChatCompletion(context.Background(), nil, &core.ChatRequest{Model: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("DispatchChatCompletion() error = nil, want provider error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider || gatewayErr.HTTPStatusCode() != http.StatusBadGateway {
		t.Fatalf("gateway error = (%s, %d), want provider 502", gatewayErr.Type, gatewayErr.HTTPStatusCode())
	}
}

func TestStreamResponsesRejectsEmptyProviderStream(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	_, err := orchestrator.StreamResponses(context.Background(), nil, &core.ResponsesRequest{Model: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("StreamResponses() error = nil, want provider error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider || gatewayErr.HTTPStatusCode() != http.StatusBadGateway {
		t.Fatalf("gateway error = (%s, %d), want provider 502", gatewayErr.Type, gatewayErr.HTTPStatusCode())
	}
}

func TestStreamResponsesFallsBackAfterEmptyPrimaryStream(t *testing.T) {
	provider := &streamFallbackProvider{
		streamsByModel: map[string]io.ReadCloser{
			"fallback": io.NopCloser(strings.NewReader("data: {}\n\n")),
		},
	}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider: provider,
		FallbackResolver: fallbackResolverFunc(func(*core.RequestModelResolution, core.Operation) []core.ModelSelector {
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
				Fallback:   true,
			},
		},
	}

	result, err := orchestrator.StreamResponses(context.Background(), workflow, &core.ResponsesRequest{Model: "primary"})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer result.Stream.Close()

	if !result.Meta.UsedFallback {
		t.Fatal("UsedFallback = false, want true")
	}
	if result.Meta.FailoverModel != "openai/fallback" {
		t.Fatalf("FailoverModel = %q, want openai/fallback", result.Meta.FailoverModel)
	}
	if got := strings.Join(provider.responseStreamCalls, ","); got != "primary,fallback" {
		t.Fatalf("response stream calls = %q, want primary,fallback", got)
	}
}

type fallbackResolverFunc func(*core.RequestModelResolution, core.Operation) []core.ModelSelector

func (f fallbackResolverFunc) ResolveFallbacks(resolution *core.RequestModelResolution, op core.Operation) []core.ModelSelector {
	return f(resolution, op)
}

type streamFallbackProvider struct {
	streamsByModel      map[string]io.ReadCloser
	responseStreamCalls []string
}

func (p *streamFallbackProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *streamFallbackProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *streamFallbackProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *streamFallbackProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *streamFallbackProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	p.responseStreamCalls = append(p.responseStreamCalls, req.Model)
	return p.streamsByModel[req.Model], nil
}

func (p *streamFallbackProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func (p *streamFallbackProvider) Supports(string) bool { return true }

func (p *streamFallbackProvider) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err == nil && selector.Provider != "" {
		return selector.Provider
	}
	return ""
}
