package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func tracedContext(t *testing.T, captureContent bool) (context.Context, *core.GenerationTrace) {
	t.Helper()
	ctx, trace := core.StartGenerationTrace(core.WithGenerationTracing(t.Context(), captureContent))
	require.NotNil(t, trace)
	return ctx, trace
}

func chatOutcome() core.GenerationOutcome {
	return core.GenerationOutcome{
		Request: &core.ChatRequest{Model: "gpt-5", Messages: []core.Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "Weather in Paris?"},
		}},
		Response: &core.ChatResponse{
			ID:    "chatcmpl-1",
			Model: "gpt-5-2025-08-07",
			Choices: []core.Choice{{
				Message:      core.ResponseMessage{Role: "assistant", Content: "Sunny."},
				FinishReason: "stop",
			}},
			Usage: core.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15},
		},
	}
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestObserverParksTracedBufferedSpanUntilOutcome(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	ctx, trace := tracedContext(t, false)
	call := llmclient.RequestInfo{Provider: "openai", ProviderType: "openai", Model: "gpt-5", Operation: "chat"}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 50*time.Millisecond, nil))
	require.Empty(t, recorder.Ended(), "the span waits for the decoded outcome")
	providerDone := time.Now()

	time.Sleep(5 * time.Millisecond)
	trace.Finish(chatOutcome())

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.False(t, spans[0].EndTime().After(providerDone), "the span keeps the provider call's end time")
	attrs := attributeMap(spans[0].Attributes())
	assert.Equal(t, "chatcmpl-1", attrs["gen_ai.response.id"])
	assert.Equal(t, "gpt-5-2025-08-07", attrs["gen_ai.response.model"])
	assert.Equal(t, "12", attrs["gen_ai.usage.input_tokens"])
	assert.Equal(t, "3", attrs["gen_ai.usage.output_tokens"])
	assert.Equal(t, "200", attrs["http.response.status_code"])
	reasons, ok := spanAttribute(spans[0], "gen_ai.response.finish_reasons")
	require.True(t, ok)
	assert.Equal(t, []string{"stop"}, reasons.AsStringSlice())
}

func TestObserverNeverExportsContentByDefault(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	ctx, trace := tracedContext(t, false)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 50*time.Millisecond, nil))
	trace.Finish(chatOutcome())

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	for _, attr := range spans[0].Attributes() {
		assert.NotContains(t, attr.Value.String(), "Paris", "attribute %s leaks the prompt", attr.Key)
		assert.NotContains(t, attr.Value.String(), "Sunny", "attribute %s leaks the completion", attr.Key)
	}
}

func TestObserverCapturesContentWhenEnabled(t *testing.T) {
	hooks, recorder, _ := newTestHooksWithCapture(t, true)
	ctx, trace := tracedContext(t, true)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 50*time.Millisecond, nil))
	trace.Finish(chatOutcome())

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attrs := attributeMap(spans[0].Attributes())
	assert.JSONEq(t, `[
		{"role":"system","parts":[{"type":"text","content":"Be brief."}]},
		{"role":"user","parts":[{"type":"text","content":"Weather in Paris?"}]}
	]`, attrs["gen_ai.input.messages"])
	assert.JSONEq(t, `[{"role":"assistant","parts":[{"type":"text","content":"Sunny."}],"finish_reason":"stop"}]`, attrs["gen_ai.output.messages"])
}

func TestObserverEndsTracedStreamWithOutcome(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	ctx, trace := tracedContext(t, false)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 40*time.Millisecond, nil))
	hooks.OnStreamFirstChunk(ctx, response(call, http.StatusOK, 60*time.Millisecond, nil))
	require.Empty(t, recorder.Ended(), "a traced stream's span ends with the stream")
	require.True(t, hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk"))

	trace.Finish(chatOutcome())
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attrs := attributeMap(spans[0].Attributes())
	assert.Equal(t, "true", attrs["gen_ai.request.stream"])
	assert.Equal(t, "12", attrs["gen_ai.usage.input_tokens"])
	assert.False(t, hasMetric(t, reader, "gen_ai.client.operation.duration"), "successful streams record time to first chunk, not duration")
}

func TestObserverFailsTracedStreamThatEndsBeforeFirstChunk(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	ctx, trace := tracedContext(t, false)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 40*time.Millisecond, nil))
	hooks.OnStreamEmpty(ctx, response(call, http.StatusOK, 60*time.Millisecond, io.EOF))
	trace.Finish(core.GenerationOutcome{})

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, "empty_stream", attributeMap(spans[0].Attributes())["error.type"])
}

func TestObserverAttachesOutcomeToTheAttemptThatServedIt(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	ctx, trace := tracedContext(t, false)
	primary := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}
	failover := llmclient.RequestInfo{Provider: "anthropic", Model: "claude-sonnet-4-5", Operation: "chat"}

	primaryCtx := hooks.OnRequestStart(ctx, primary)
	hooks.OnRequestEnd(primaryCtx, response(primary, http.StatusServiceUnavailable, 30*time.Millisecond, nil))
	failoverCtx := hooks.OnRequestStart(ctx, failover)
	hooks.OnRequestEnd(failoverCtx, response(failover, http.StatusOK, 30*time.Millisecond, nil))
	trace.Finish(chatOutcome())

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	primaryAttrs, failoverAttrs := attributeMap(spans[0].Attributes()), attributeMap(spans[1].Attributes())
	assert.Equal(t, "503", primaryAttrs["error.type"])
	assert.NotContains(t, primaryAttrs, "gen_ai.usage.input_tokens")
	assert.Equal(t, "anthropic", failoverAttrs["gomodel.provider.name"])
	assert.Equal(t, "12", failoverAttrs["gen_ai.usage.input_tokens"])
}

func TestObserverRecordsResponsesOutcome(t *testing.T) {
	hooks, recorder, _ := newTestHooksWithCapture(t, true)
	ctx, trace := tracedContext(t, true)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 50*time.Millisecond, nil))
	trace.Finish(core.GenerationOutcome{
		Request: &core.ResponsesRequest{Model: "gpt-5", Instructions: "Be brief.", Input: "Hi"},
		Response: &core.ResponsesResponse{
			ID:     "resp_1",
			Model:  "gpt-5",
			Status: "incomplete",
			IncompleteDetails: &core.ResponsesIncompleteDetails{
				Reason: "max_output_tokens",
			},
			Output: []core.ResponsesOutputItem{{
				Type:    "message",
				Role:    "assistant",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "Hel"}},
			}},
			Usage: &core.ResponsesUsage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9},
		},
	})

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attrs := attributeMap(spans[0].Attributes())
	assert.Equal(t, "7", attrs["gen_ai.usage.input_tokens"])
	assert.Equal(t, "2", attrs["gen_ai.usage.output_tokens"])
	assert.JSONEq(t, `[{"type":"text","content":"Be brief."}]`, attrs["gen_ai.system_instructions"])
	assert.JSONEq(t, `[{"role":"user","parts":[{"type":"text","content":"Hi"}]}]`, attrs["gen_ai.input.messages"])
	assert.JSONEq(t, `[{"role":"assistant","parts":[{"type":"text","content":"Hel"}],"finish_reason":"max_output_tokens"}]`, attrs["gen_ai.output.messages"])
}

func TestResponsesFinishReason(t *testing.T) {
	tests := []struct {
		name string
		resp core.ResponsesResponse
		want string
	}{
		{name: "completed", resp: core.ResponsesResponse{Status: "completed"}, want: "stop"},
		{name: "incomplete with reason", resp: core.ResponsesResponse{Status: "incomplete", IncompleteDetails: &core.ResponsesIncompleteDetails{Reason: "content_filter"}}, want: "content_filter"},
		{name: "incomplete without reason", resp: core.ResponsesResponse{Status: "incomplete"}, want: "incomplete"},
		{name: "failed", resp: core.ResponsesResponse{Status: "failed"}, want: "failed"},
		{name: "background still running", resp: core.ResponsesResponse{Status: "in_progress"}, want: ""},
		{name: "queued", resp: core.ResponsesResponse{Status: "queued"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, responsesFinishReason(&tt.resp))
		})
	}
}

func TestObserverOmitsUsageWhenProviderReportedNone(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	ctx, trace := tracedContext(t, false)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}

	ctx = hooks.OnRequestStart(ctx, call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 50*time.Millisecond, nil))
	trace.Finish(core.GenerationOutcome{Response: &core.ChatResponse{ID: "chatcmpl-1"}})

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.NotContains(t, attributeMap(spans[0].Attributes()), "gen_ai.usage.input_tokens")
}

func TestContentAttributesConvertMessages(t *testing.T) {
	tests := []struct {
		name    string
		outcome core.GenerationOutcome
		key     string
		want    string
	}{
		{
			name: "chat tool round trip and media placeholders",
			outcome: core.GenerationOutcome{Request: &core.ChatRequest{Messages: []core.Message{
				{Role: "user", Content: []core.ContentPart{
					{Type: "text", Text: "What is this?"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "data:image/png;base64,AAAA"}},
				}},
				{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "lookup", Arguments: `{"q":"x"}`}}}},
				{Role: "tool", ToolCallID: "call_1", Content: "found"},
			}}},
			key: "gen_ai.input.messages",
			want: `[
				{"role":"user","parts":[{"type":"text","content":"What is this?"},{"type":"image_url"}]},
				{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"lookup","arguments":{"q":"x"}}]},
				{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1","response":"found"}]}
			]`,
		},
		{
			name: "chat tool call output with unparseable arguments",
			outcome: core.GenerationOutcome{Response: &core.ChatResponse{Choices: []core.Choice{{
				Message:      core.ResponseMessage{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_2", Function: core.FunctionCall{Name: "lookup", Arguments: `{"q":`}}}},
				FinishReason: "tool_calls",
			}}}},
			key:  "gen_ai.output.messages",
			want: `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_2","name":"lookup","arguments":"{\"q\":"}],"finish_reason":"tool_calls"}]`,
		},
		{
			name: "responses typed input items",
			outcome: core.GenerationOutcome{Request: &core.ResponsesRequest{Input: []core.ResponsesInputElement{
				{Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "Hi"}, map[string]any{"type": "input_image", "image_url": "https://example.com/a.png"}}},
				{Type: "function_call", CallID: "call_1", Name: "lookup", Arguments: `{}`},
				{Type: "function_call_output", CallID: "call_1", Output: "42"},
			}}},
			key: "gen_ai.input.messages",
			want: `[
				{"role":"user","parts":[{"type":"text","content":"Hi"},{"type":"input_image"}]},
				{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"lookup","arguments":{}}]},
				{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1","response":"42"}]}
			]`,
		},
		{
			name: "responses generic input items",
			outcome: core.GenerationOutcome{Request: &core.ResponsesRequest{Input: []any{
				map[string]any{"role": "developer", "content": "Rules."},
			}}},
			key:  "gen_ai.input.messages",
			want: `[{"role":"developer","parts":[{"type":"text","content":"Rules."}]}]`,
		},
		{
			name: "responses function call output",
			outcome: core.GenerationOutcome{Response: &core.ResponsesResponse{Status: "completed", Output: []core.ResponsesOutputItem{
				{Type: "reasoning"},
				{Type: "function_call", CallID: "call_3", Name: "lookup", Arguments: `{"q":"y"}`},
			}}},
			key:  "gen_ai.output.messages",
			want: `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_3","name":"lookup","arguments":{"q":"y"}}],"finish_reason":"stop"}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := attributeMap(contentAttributes(tt.outcome))
			require.Contains(t, attrs, tt.key)
			assert.JSONEq(t, tt.want, attrs[tt.key])
		})
	}
}

func TestLimitPartsChargesMarkersForOversizedArguments(t *testing.T) {
	arguments := json.RawMessage(`{"q":"` + strings.Repeat("a", 64) + `"}`)
	tests := []struct {
		name      string
		remaining int
		want      any
		left      int
	}{
		{name: "fits", remaining: len(arguments), want: arguments, left: 0},
		{name: "marker fits", remaining: 20, want: truncatedMarker, left: 20 - len(truncatedMarker)},
		{name: "nothing fits", remaining: 5, want: nil, left: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := []genAIPart{{Type: "tool_call", Arguments: arguments}}
			left := limitParts(parts, tt.remaining)
			assert.Equal(t, tt.want, parts[0].Arguments)
			assert.Equal(t, tt.left, left)
		})
	}
}

func TestContentAttributesTruncateLongText(t *testing.T) {
	long := strings.Repeat("é", maxContentBytes)
	attrs := attributeMap(contentAttributes(core.GenerationOutcome{
		Request: &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: long}}},
	}))

	var messages []genAIMessage
	require.NoError(t, json.Unmarshal([]byte(attrs["gen_ai.input.messages"]), &messages))
	require.Len(t, messages, 1)
	content := messages[0].Parts[0].Content
	assert.Less(t, len(content), len(long))
	require.True(t, strings.HasSuffix(content, truncatedMarker))
	assert.LessOrEqual(t, len(content), maxContentBytes, "the marker fits within the limit")
	kept := strings.TrimSuffix(content, truncatedMarker)
	assert.True(t, strings.HasPrefix(long, kept) && utf8.ValidString(kept), "text is cut at a rune boundary")
}

func TestContentAttributesKeepNewestMessagesWithinAttributeBudget(t *testing.T) {
	part := strings.Repeat("a", maxContentBytes)
	messages := make([]core.Message, 0, 12)
	for range 11 {
		messages = append(messages, core.Message{Role: "user", Content: part})
	}
	messages = append(messages, core.Message{Role: "user", Content: "latest question"})
	attrs := attributeMap(contentAttributes(core.GenerationOutcome{
		Request: &core.ChatRequest{Messages: messages},
	}))

	var got []genAIMessage
	require.NoError(t, json.Unmarshal([]byte(attrs["gen_ai.input.messages"]), &got))
	require.Len(t, got, len(messages), "every message keeps its place")
	assert.Equal(t, "latest question", got[len(got)-1].Parts[0].Content)
	assert.Equal(t, part, got[len(got)-2].Parts[0].Content, "recent messages stay intact")
	assert.Empty(t, got[0].Parts[0].Content, "the oldest text is dropped first")
	assert.Equal(t, 1, strings.Count(attrs["gen_ai.input.messages"], truncatedMarker), "only the message cut at the budget's edge is marked")

	total := 0
	for _, message := range got {
		for _, p := range message.Parts {
			total += len(p.Content)
		}
	}
	assert.LessOrEqual(t, total, maxAttributeContentBytes, "markers count toward the budget")
}
