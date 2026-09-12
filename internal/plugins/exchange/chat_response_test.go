package exchange

import (
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goccy/go-json"
)

const chatResponseFixture = `{"id":"r1","object":"chat.completion","model":"m","provider":"p","choices":[{"index":0,"message":{"role":"assistant","content":"hello world","reasoning_content":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls","native_finish":"x"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}},"created":1}`

func chatCompletion(t *testing.T) (*core.ChatResponse, *pluginapi.Completion) {
	t.Helper()
	var resp core.ChatResponse
	err := json.Unmarshal([]byte(chatResponseFixture), &resp)
	require.NoError(t, err)

	c, err := FromChatResponse(&resp)
	require.NoError(t, err)

	return &resp, c
}

func TestFromChatResponse(t *testing.T) {
	_, c := chatCompletion(t)
	require.Equal(t, "r1", c.ID)
	require.Equal(t, "m", c.Model)
	require.Len(t, c.Choices, 1)

	parts := c.Choices[0].Message.Parts
	assert.Len(t, parts, 3)
	assert.Equal(t, pluginapi.PartReasoning, parts[0].Kind)
	assert.Equal(t, "think", parts[0].Text)
	assert.Equal(t, "hello world", parts[1].Text)
	assert.Equal(t, pluginapi.PartToolCall, parts[2].Kind)
	assert.Equal(t, "c1", parts[2].ToolCall.ID)
	assert.Equal(t, "tool_calls", c.Choices[0].FinishReason)
	assert.Equal(t, "choice:0", c.Choices[0].Message.ID)
	assert.Equal(t, pluginapi.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CachedInputTokens: 4}, c.Usage)
	assert.Equal(t, "hello world", c.Text(0))
	assert.False(t, c.Changes().Dirty)
}

func TestApplyToChatResponse(t *testing.T) {
	// Expectations are expressed as substitutions on the canonical encoding
	// of the original, so field order is whatever core emits.
	tests := []struct {
		name     string
		edit     func(c *pluginapi.Completion) error
		from, to string
	}{
		{name: "no edits", edit: func(*pluginapi.Completion) error { return nil }},
		{
			name: "set text keeps extras",
			edit: func(c *pluginapi.Completion) error { return c.SetText(0, 1, "bye") },
			from: `"content":"hello world"`, to: `"content":"bye"`,
		},
		{
			name: "finish reason",
			edit: func(c *pluginapi.Completion) error { return c.SetFinishReason(0, "content_filter") },
			from: `"finish_reason":"tool_calls"`, to: `"finish_reason":"content_filter"`,
		},
		{
			name: "replace text",
			edit: func(c *pluginapi.Completion) error { return c.ReplaceText(0, "[x]") },
			from: `"content":"hello world"`, to: `"content":"[x]"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, c := chatCompletion(t)
			canonical := string(mustJSON(t, resp))
			want := canonical
			if tt.from != "" {
				require.Contains(t, canonical, tt.from)

				want = strings.Replace(canonical, tt.from, tt.to, 1)
			}
			err := tt.edit(c)
			require.NoError(t, err)

			applied, err := ApplyToChatResponse(resp, c)
			require.NoError(t, err)
			got := string(mustJSON(t, applied))
			assert.Equal(t, want, got)
			// The original is never mutated.
			got = string(mustJSON(t, resp))
			assert.Equal(t, canonical, got)
		})
	}
}

func TestCompletionToChatResponse(t *testing.T) {
	resp := CompletionToChatResponse(pluginapi.Respond("nope").Response, "m")
	assert.True(t, strings.HasPrefix(resp.ID, "gomodel-plugin-"))
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "m", resp.Model)
	assert.NotEqual(t, int64(0), resp.Created, "envelope = %+v", resp)
	assert.Len(t, resp.Choices, 1)
	assert.Equal(t, "nope", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, 0, resp.Usage.TotalTokens)
	_, err := json.Marshal(resp)
	assert.NoError(t, err)

	empty := CompletionToChatResponse(nil, "m")
	assert.Len(t, empty.Choices, 1)
}

func TestApplyToChatResponseToolArguments(t *testing.T) {
	resp, c := chatCompletion(t)
	err := c.SetToolArguments(0, "c1", json.RawMessage(`{"to":"a@b.c"}`))
	require.NoError(t, err)

	applied, err := ApplyToChatResponse(resp, c)
	require.NoError(t, err)
	got := applied.Choices[0].Message.ToolCalls[0].Function.Arguments
	assert.Equal(t, `{"to":"a@b.c"}`, got)
	assert.Equal(t, "{}", resp.Choices[0].Message.ToolCalls[0].Function.Arguments)

	// A replaced text keeps the argument edit, whichever came first.
	for _, first := range []string{"replace", "arguments"} {
		resp, c := chatCompletion(t)
		edits := []func() error{
			func() error { return c.ReplaceText(0, "[x]") },
			func() error { return c.SetToolArguments(0, "c1", json.RawMessage(`{"to":"a@b.c"}`)) },
		}
		if first == "arguments" {
			edits[0], edits[1] = edits[1], edits[0]
		}
		for _, edit := range edits {
			err := edit()
			require.NoError(t, err)
		}
		applied, err := ApplyToChatResponse(resp, c)
		require.NoError(t, err)
		assert.Equal(t, `{"to":"a@b.c"}`, applied.Choices[0].Message.ToolCalls[0].Function.Arguments)
		assert.Equal(t, "[x]", core.ExtractTextContent(applied.Choices[0].Message.Content), "%s first: %+v", first, applied.Choices[0].Message)
	}
	assert.Error(t, c.SetToolArguments(0, "nope", json.RawMessage(`{}`)))
	assert.Error(t, c.SetToolArguments(0, "c1", json.RawMessage(`{`)))
}
