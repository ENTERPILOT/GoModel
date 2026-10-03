package gateway

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// tracingProvider parks a completion in the request's generation trace the
// way telemetry does for a successful provider call, recording the outcome.
type tracingProvider struct {
	providerTypeResolverStub
	stream   string
	outcomes []core.GenerationOutcome
}

func (p *tracingProvider) park(ctx context.Context) {
	core.GenerationTraceFromContext(ctx).Park(func(outcome core.GenerationOutcome) {
		p.outcomes = append(p.outcomes, outcome)
	})
}

func (p *tracingProvider) ChatCompletion(ctx context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	p.park(ctx)
	return p.chatResponse, nil
}

func (p *tracingProvider) StreamChatCompletion(ctx context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	p.park(ctx)
	return io.NopCloser(strings.NewReader(p.stream)), nil
}

func (p *tracingProvider) StreamResponses(ctx context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	p.park(ctx)
	return io.NopCloser(strings.NewReader(p.stream)), nil
}

func tracingWorkflow() *core.Workflow {
	return &core.Workflow{ProviderType: "openai"}
}

func TestExecuteChatCompletionFinishesGenerationTrace(t *testing.T) {
	resp := &core.ChatResponse{ID: "chatcmpl-1", Model: "gpt-5", Choices: []core.Choice{{FinishReason: "stop"}}}
	provider := &tracingProvider{chatResponse: resp}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: provider})
	req := &core.ChatRequest{Model: "gpt-5"}

	ctx := core.WithGenerationTracing(t.Context(), false)
	_, err := orchestrator.ExecuteChatCompletion(ctx, tracingWorkflow(), req, "req-1", "/v1/chat/completions")
	require.NoError(t, err)

	require.Len(t, provider.outcomes, 1)
	assert.Same(t, req, provider.outcomes[0].Request)
	assert.Same(t, resp, provider.outcomes[0].Response)
}

func TestExecuteChatCompletionFinishesGenerationTraceOnRejectedResponse(t *testing.T) {
	provider := &tracingProvider{chatResponse: &core.ChatResponse{ID: "chatcmpl-empty"}}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: provider})

	ctx := core.WithGenerationTracing(t.Context(), false)
	_, err := orchestrator.ExecuteChatCompletion(ctx, tracingWorkflow(), &core.ChatRequest{Model: "gpt-5"}, "req-1", "/v1/chat/completions")
	require.Error(t, err)

	require.Len(t, provider.outcomes, 1, "the parked call still ends")
	assert.Nil(t, provider.outcomes[0].Response)
}

func TestStreamChatCompletionFinishesGenerationTraceWhenStreamCloses(t *testing.T) {
	provider := &tracingProvider{stream: strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		`data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\""}}]}}]}`,
		`data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl-1","model":"gpt-5","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: provider})
	req := &core.ChatRequest{Model: "gpt-5", Stream: true}

	ctx := core.WithGenerationTracing(t.Context(), true)
	result, err := orchestrator.StreamChatCompletion(ctx, tracingWorkflow(), req)
	require.NoError(t, err)
	observer := result.GenerationObserver()
	require.NotNil(t, observer)

	stream := streaming.NewObservedSSEStream(result.Stream, observer)
	_, err = io.Copy(io.Discard, stream)
	require.NoError(t, err)
	require.Empty(t, provider.outcomes, "the trace waits for the stream to close")
	require.NoError(t, stream.Close())

	require.Len(t, provider.outcomes, 1)
	assert.Same(t, req, provider.outcomes[0].Request)
	resp, ok := provider.outcomes[0].Response.(*core.ChatResponse)
	require.True(t, ok)
	assert.Equal(t, "chatcmpl-1", resp.ID)
	assert.Equal(t, "gpt-5", resp.Model)
	assert.Equal(t, core.Usage{PromptTokens: 9, CompletionTokens: 4, TotalTokens: 13}, resp.Usage)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "tool_calls", resp.Choices[0].FinishReason)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, "Hello", resp.Choices[0].Message.Content)
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, core.ToolCall{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "lookup", Arguments: `{"q":1}`}}, resp.Choices[0].Message.ToolCalls[0])
}

func TestStreamResponsesFinishesGenerationTraceWithCompletedResponse(t *testing.T) {
	provider := &tracingProvider{stream: strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"Hi"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
		``,
		``,
	}, "\n")}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: provider})
	req := &core.ResponsesRequest{Model: "gpt-5", Stream: true}

	ctx := core.WithGenerationTracing(t.Context(), false)
	result, err := orchestrator.StreamResponses(ctx, tracingWorkflow(), req)
	require.NoError(t, err)

	stream := streaming.NewObservedSSEStream(result.Stream, result.GenerationObserver())
	_, err = io.Copy(io.Discard, stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	require.Len(t, provider.outcomes, 1)
	resp, ok := provider.outcomes[0].Response.(*core.ResponsesResponse)
	require.True(t, ok)
	assert.Equal(t, "resp_1", resp.ID)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 5, resp.Usage.InputTokens)
	assert.Equal(t, 1, resp.Usage.OutputTokens)
}

func TestGenerationStreamObserverBoundsCapturedContent(t *testing.T) {
	_, trace := core.StartGenerationTrace(core.WithGenerationTracing(t.Context(), true))
	observer := newChatGenerationObserver(trace, &core.ChatRequest{})
	chunk := strings.Repeat("x", 1024)
	for range 2 * core.GenerationContentLimit / len(chunk) {
		observer.OnJSONEvent(map[string]any{"choices": []any{map[string]any{
			"index": float64(0),
			"delta": map[string]any{
				"content":    chunk,
				"tool_calls": []any{map[string]any{"index": float64(0), "function": map[string]any{"arguments": chunk}}},
			},
		}}})
	}

	message := observer.chatResponse().Choices[0].Message
	assert.Len(t, message.Content, core.GenerationContentLimit+1, "text stops growing one byte past the export limit")
	assert.Len(t, message.ToolCalls[0].Function.Arguments, core.GenerationContentLimit+1)
}

func TestStreamResultWithoutTracingHasNoGenerationObserver(t *testing.T) {
	provider := &tracingProvider{stream: "data: [DONE]\n\n"}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: provider})

	result, err := orchestrator.StreamChatCompletion(t.Context(), tracingWorkflow(), &core.ChatRequest{Model: "gpt-5", Stream: true})
	require.NoError(t, err)
	assert.Nil(t, result.GenerationObserver())
}

func TestGenerationStreamObserverDecodesOnlyWhatItNeeds(t *testing.T) {
	_, trace := core.StartGenerationTrace(core.WithGenerationTracing(t.Context(), false))
	_, capturing := core.StartGenerationTrace(core.WithGenerationTracing(t.Context(), true))
	contentChunk := []byte(`{"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}],"usage":null}`)
	finishChunk := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	spacedFinishChunk := []byte(`{"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}`)
	usageChunk := []byte(`{"choices":[],"usage":{"prompt_tokens":1}}`)

	chat := newChatGenerationObserver(trace, &core.ChatRequest{})
	assert.False(t, chat.WantsJSONEvent(contentChunk))
	assert.True(t, chat.WantsJSONEvent(finishChunk))
	assert.True(t, chat.WantsJSONEvent(spacedFinishChunk))
	assert.True(t, chat.WantsJSONEvent(usageChunk))
	assert.True(t, newChatGenerationObserver(capturing, &core.ChatRequest{}).WantsJSONEvent(contentChunk))

	responses := newResponsesGenerationObserver(capturing, &core.ResponsesRequest{})
	assert.False(t, responses.WantsJSONEvent([]byte(`{"type":"response.output_text.delta","delta":"Hi"}`)))
	assert.True(t, responses.WantsJSONEvent([]byte(`{"type":"response.incomplete","response":{}}`)))
}
