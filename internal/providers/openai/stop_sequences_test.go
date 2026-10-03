package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func stopFields(raw string) core.UnknownJSONFields {
	return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"stop": json.RawMessage(raw)})
}

func TestTakeStopSequences(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		stop      string
		wantStops []string
		wantKept  bool // stop stays on the request
		wantErr   bool
	}{
		{name: "string", model: "gpt-6-luna", stop: `"END"`, wantStops: []string{"END"}},
		{name: "array without blanks", model: "gpt-5.5", stop: `["a","","b"]`, wantStops: []string{"a", "b"}},
		{name: "null", model: "o4-mini", stop: `null`},
		{name: "model that accepts stop", model: "gpt-4o", stop: `"END"`, wantKept: true},
		{name: "invalid", model: "gpt-6-luna", stop: `42`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{Model: tt.model, ExtraFields: stopFields(tt.stop)}
			got, stops, err := takeStopSequences(req)
			if tt.wantErr {
				var gwErr *core.GatewayError
				require.ErrorAs(t, err, &gwErr)
				assert.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKept, got.ExtraFields.HasAny("stop"))
			if !tt.wantKept {
				assert.ElementsMatch(t, tt.wantStops, stops)
				assert.True(t, req.ExtraFields.HasAny("stop"), "the caller's request must not change")
			}
		})
	}
}

// The chat path sends no stop to a model that rejects it and cuts the answer
// at the earliest stop sequence, reporting it.
func TestChatCompletion_EmulatesStopSequences(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-6-luna",
		"choices":[{"index":0,"message":{"role":"assistant","content":"1 2 3 4 5 6 7"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`)
	provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-6-luna", Messages: []core.Message{{Role: "user", Content: "count"}}, ExtraFields: stopFields(`["6","4"]`),
	})
	require.NoError(t, err)
	assert.NotContains(t, capture.Last(t).JSON(t), "stop")
	choice := resp.Choices[0]
	assert.Equal(t, "1 2 3 ", choice.Message.Content)
	assert.Equal(t, "stop", choice.FinishReason)
	assert.Equal(t, "4", choice.StopSequence)
	assert.Equal(t, 7, resp.Usage.CompletionTokens, "usage stays the provider's")
}

func chunkLine(index int, delta map[string]any, finish any) string {
	raw, _ := json.Marshal(map[string]any{"id": "c1", "object": "chat.completion.chunk", "model": "m",
		"choices": []any{map[string]any{"index": index, "delta": delta, "finish_reason": finish}}})
	return "data: " + string(raw) + "\n\n"
}

type streamResult struct {
	text, stopSequence, finish string
	sawUsage, sawDone          bool
}

func runStopStream(t *testing.T, stops []string, upstream string) streamResult {
	t.Helper()
	stream := newStopSequenceStream(io.NopCloser(strings.NewReader(upstream)), stops)
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)

	var got streamResult
	for line := range strings.SplitSeq(string(body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			got.sawDone = true
			continue
		}
		var chunk map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &chunk))
		if chunk["usage"] != nil {
			got.sawUsage = true
		}
		for _, raw := range chunk["choices"].([]any) {
			choice := raw.(map[string]any)
			delta := choice["delta"].(map[string]any)
			if content, ok := delta["content"].(string); ok {
				got.text += content
			}
			if stop, ok := delta["stop_sequence"].(string); ok {
				got.stopSequence = stop
			}
			if finish, ok := choice["finish_reason"].(string); ok {
				require.Empty(t, got.finish, "a choice finishes once")
				got.finish = finish
			}
		}
	}
	return got
}

func TestStopSequenceStream(t *testing.T) {
	usage := `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":9}}` + "\n\n"
	done := "data: [DONE]\n\n"
	text := func(s string) map[string]any { return map[string]any{"content": s} }
	tests := []struct {
		name     string
		stops    []string
		upstream string
		want     streamResult
	}{
		{name: "match split across chunks", stops: []string{"END"},
			upstream: chunkLine(0, map[string]any{"role": "assistant", "content": ""}, nil) +
				chunkLine(0, text("one two E"), nil) + chunkLine(0, text("ND three"), nil) +
				chunkLine(0, map[string]any{}, "stop") + usage + done,
			want: streamResult{text: "one two ", stopSequence: "END", finish: "stop", sawUsage: true, sawDone: true}},
		{name: "no match flushes the held-back text", stops: []string{"END"},
			upstream: chunkLine(0, text("one E"), nil) + chunkLine(0, text("N"), nil) +
				chunkLine(0, map[string]any{}, "length") + usage + done,
			want: streamResult{text: "one EN", finish: "length", sawUsage: true, sawDone: true}},
		{name: "multibyte text is never split", stops: []string{"żółw"},
			upstream: chunkLine(0, text("zażółć ż"), nil) + chunkLine(0, text("ółw gęślą"), nil) +
				chunkLine(0, map[string]any{}, "stop") + done,
			want: streamResult{text: "zażółć ", stopSequence: "żółw", finish: "stop", sawDone: true}},
		{name: "text after the stop is dropped", stops: []string{"5"},
			upstream: chunkLine(0, text("1 2 3 4 5 6"), nil) + chunkLine(0, text(" 7 8"), nil) +
				chunkLine(0, map[string]any{}, "stop") + usage + done,
			want: streamResult{text: "1 2 3 4 ", stopSequence: "5", finish: "stop", sawUsage: true, sawDone: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, runStopStream(t, tt.stops, tt.upstream))
		})
	}
}

// Held-back text is flushed ahead of a tool call, so the order is kept.
func TestStopSequenceStream_FlushesBeforeToolCalls(t *testing.T) {
	call := map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
		"function": map[string]any{"name": "f", "arguments": "{}"}}}}
	upstream := chunkLine(0, map[string]any{"content": "Checking E"}, nil) + chunkLine(0, call, nil) +
		chunkLine(0, map[string]any{}, "tool_calls") + "data: [DONE]\n\n"
	got := runStopStream(t, []string{"END"}, upstream)
	assert.Equal(t, "Checking E", got.text)
	assert.Equal(t, "tool_calls", got.finish)
}

// A stop sequence cuts the text but never a tool call: the caller needs it to
// continue its tool loop, and the turn finishes as a tool call.
func TestStopSequences_KeepToolCalls(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		call := map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "f", "arguments": "{}"}}}}
		upstream := chunkLine(0, map[string]any{"content": "Plan: END then call"}, nil) + chunkLine(0, call, nil) +
			chunkLine(0, map[string]any{}, "tool_calls") + "data: [DONE]\n\n"
		stream := newStopSequenceStream(io.NopCloser(strings.NewReader(upstream)), []string{"END"})
		body, err := io.ReadAll(stream)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"call_1"`)

		got := runStopStream(t, []string{"END"}, upstream)
		assert.Equal(t, "Plan: ", got.text)
		assert.Equal(t, "tool_calls", got.finish)
		assert.Empty(t, got.stopSequence)
	})
	t.Run("blocking", func(t *testing.T) {
		resp := &core.ChatResponse{Choices: []core.Choice{{
			Message: core.ResponseMessage{Role: "assistant", Content: "Plan: END then call",
				ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"}}}},
			FinishReason: "tool_calls",
		}}}
		applyStopSequences(resp, []string{"END"})
		choice := resp.Choices[0]
		assert.Equal(t, "Plan: ", choice.Message.Content)
		assert.Len(t, choice.Message.ToolCalls, 1)
		assert.Equal(t, "tool_calls", choice.FinishReason)
		assert.Empty(t, choice.StopSequence)
	})
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// An upstream read error is returned after the data before it, never turned
// into a clean end of stream.
func TestStopSequenceStream_KeepsUpstreamError(t *testing.T) {
	broken := errors.New("connection reset")
	upstream := io.MultiReader(strings.NewReader(chunkLine(0, map[string]any{"content": "partial"}, nil)), failingReader{err: broken})
	stream := newStopSequenceStream(io.NopCloser(upstream), []string{"END"})
	body, err := io.ReadAll(stream)
	require.ErrorIs(t, err, broken)
	// "al" stays held back: it could still begin "END" when the stream broke.
	assert.Contains(t, string(body), `"parti"`)
}

// Text without a stop sequence, and content that is not text, are left as is.
func TestApplyStopSequences_LeavesOtherChoices(t *testing.T) {
	parts := []core.ContentPart{{Type: "text", Text: "a END b"}}
	resp := &core.ChatResponse{Choices: []core.Choice{
		{Message: core.ResponseMessage{Role: "assistant", Content: "no stop here"}, FinishReason: "length"},
		{Index: 1, Message: core.ResponseMessage{Role: "assistant", Content: parts}, FinishReason: "stop"},
	}}
	applyStopSequences(resp, []string{"END"})
	assert.Equal(t, "no stop here", resp.Choices[0].Message.Content)
	assert.Equal(t, "length", resp.Choices[0].FinishReason)
	assert.Equal(t, parts, resp.Choices[1].Message.Content)
	assert.Empty(t, resp.Choices[1].StopSequence)
}
