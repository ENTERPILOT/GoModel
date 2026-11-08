package mistral

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestFlattenThinkingChunks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "message content array flattens to string plus reasoning_content",
			in:   `{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"hmm"}]},{"type":"text","text":"42"}]}}]}`,
			want: `{"choices":[{"index":0,"message":{"content":"42","reasoning_content":"hmm","role":"assistant"}}]}`,
		},
		{
			name: "delta content array in a stream chunk",
			in:   `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":"","reasoning_content":"ab"}}],"object":"chat.completion.chunk"}`,
		},
		{
			name: "transition chunk with thinking and text in one delta",
			in:   `{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"t"}],"signature":"sig"},{"type":"text","text":"answer"}]}}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":"answer","reasoning_content":"t"}}]}`,
		},
		{
			name: "string content is untouched",
			in:   `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			want: `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		},
		{
			name: "null content is untouched",
			in:   `{"choices":[{"index":0,"message":{"role":"assistant","content":null}}]}`,
			want: `{"choices":[{"index":0,"message":{"role":"assistant","content":null}}]}`,
		},
		{
			name: "unknown chunk types move to the vendor member, content stays a string",
			in:   `{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"see "},{"type":"reference","reference_ids":[7]}]}}]}`,
			want: `{"choices":[{"index":0,"message":{"role":"assistant","content":"see ","mistral_content_chunks":[{"type":"reference","reference_ids":[7]}]}}]}`,
		},
		{
			name: "non-chat payloads are untouched",
			in:   `{"object":"list","data":[{"id":"mistral-large-latest"}]}`,
			want: `{"object":"list","data":[{"id":"mistral-large-latest"}]}`,
		},
		{
			name: "not JSON",
			in:   `[DONE]`,
			want: `[DONE]`,
		},
		{
			name: "thinking as a plain string is tolerated",
			in:   `{"choices":[{"index":0,"message":{"content":[{"type":"thinking","thinking":"raw"},{"type":"text","text":"ok"}]}}]}`,
			want: `{"choices":[{"index":0,"message":{"content":"ok","reasoning_content":"raw"}}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenThinkingChunks([]byte(tt.in))
			if json.Valid([]byte(tt.want)) {
				assert.JSONEq(t, tt.want, string(got))
			} else {
				assert.Equal(t, tt.want, string(got))
			}
		})
	}
}

func TestFlattenThinkingChunks_PreservesOtherMembers(t *testing.T) {
	in := `{"id":"cmpl-1","object":"chat.completion.chunk","model":"mistral-large-4-0","created":1773842038,"choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"x"}]}]},"finish_reason":null}],"usage":null}`
	got := string(flattenThinkingChunks([]byte(in)))
	assert.Contains(t, got, `"id":"cmpl-1"`)
	assert.Contains(t, got, `"model":"mistral-large-4-0"`)
	assert.Contains(t, got, `"created":1773842038`)
	assert.Contains(t, got, `"role":"assistant"`)
	assert.Contains(t, got, `"reasoning_content":"x"`)
}

func TestAdaptChatRequest(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		got, err := adaptChatRequest(nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("reasoning effort passes through lowercased", func(t *testing.T) {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "MAX"} {
			req := &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Effort: effort}}
			got, err := adaptChatRequest(req)
			require.NoError(t, err)
			assert.Nil(t, got.Reasoning)
			assert.Equal(t, json.RawMessage(`"`+strings.ToLower(effort)+`"`), got.ExtraFields.Lookup("reasoning_effort"), "effort %q", effort)
		}
	})

	t.Run("no reasoning leaves the request identical", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hi"}}}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		assert.Same(t, req, got)
	})

	t.Run("reasoning without effort is dropped, not forwarded", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Summary: "auto"}}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		assert.Nil(t, got.Reasoning, "Mistral rejects the unknown nested reasoning member with a 422")
		assert.Nil(t, got.ExtraFields.Lookup("reasoning_effort"))
	})

	t.Run("assistant reasoning_content and vendor chunks are stripped, other roles untouched", func(t *testing.T) {
		req := &core.ChatRequest{
			Model: "m",
			Messages: []core.Message{
				{Role: "user", Content: "hi", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					reasoningContentKey: json.RawMessage(`"must stay"`),
				})},
				{Role: "assistant", Content: "hello", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					reasoningContentKey: json.RawMessage(`"drop me"`),
					unknownChunksKey:    json.RawMessage(`[{"type":"reference"}]`),
					"other":             json.RawMessage(`1`),
				})},
			},
		}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		require.NotSame(t, req, got)
		assert.NotNil(t, got.Messages[0].ExtraFields.Lookup(reasoningContentKey), "user message keeps its extra")
		assert.Nil(t, got.Messages[1].ExtraFields.Lookup(reasoningContentKey))
		assert.Nil(t, got.Messages[1].ExtraFields.Lookup(unknownChunksKey), "Mistral 422s on the echoed vendor member too")
		assert.Equal(t, json.RawMessage(`1`), got.Messages[1].ExtraFields.Lookup("other"), "other members survive")
		// The caller's request is not mutated.
		assert.NotNil(t, req.Messages[1].ExtraFields.Lookup(reasoningContentKey))
	})
}
