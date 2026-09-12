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

const responsesResponseFixture = `{"id":"resp_1","object":"response","created_at":1,"model":"m","provider":"p","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]},{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]},{"type":"output_text","text":" world","annotations":[]}]},{"id":"fc_1","type":"function_call","call_id":"c1","name":"f","arguments":"{\"a\":1}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`

func responsesCompletion(t *testing.T) (*core.ResponsesResponse, *pluginapi.Completion) {
	t.Helper()
	var resp core.ResponsesResponse
	err := json.Unmarshal([]byte(responsesResponseFixture), &resp)
	require.NoError(t, err)

	c, err := FromResponsesResponse(&resp)
	require.NoError(t, err)

	return &resp, c
}

func TestFromResponsesResponse(t *testing.T) {
	_, c := responsesCompletion(t)
	require.Len(t, c.Choices, 1)
	require.Equal(t, "tool_calls", c.Choices[0].FinishReason)

	parts := c.Choices[0].Message.Parts
	assert.Len(t, parts, 4)
	assert.Equal(t, pluginapi.PartReasoning, parts[0].Kind)
	assert.Equal(t, "thinking", parts[0].Text)
	assert.Equal(t, "hello", parts[1].Text)
	assert.Equal(t, " world", parts[2].Text)
	assert.Equal(t, pluginapi.PartToolCall, parts[3].Kind)
	assert.Equal(t, `{"a":1}`, string(parts[3].ToolCall.Arguments))
	assert.Equal(t, pluginapi.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, c.Usage)
	assert.Equal(t, "hello world", c.Text(0))

	plain, err := FromResponsesResponse(&core.ResponsesResponse{Status: "incomplete"})
	assert.NoError(t, err)
	assert.Equal(t, "length", plain.Choices[0].FinishReason)
}

func TestApplyToResponsesResponse(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(c *pluginapi.Completion) error
		from, to string
	}{
		{name: "no edits", edit: func(*pluginapi.Completion) error { return nil }},
		{
			name: "set text in place",
			edit: func(c *pluginapi.Completion) error { return c.SetText(0, 2, " there") },
			from: `"text":" world"`, to: `"text":" there"`,
		},
		{
			name: "finish reason leaves status alone",
			edit: func(c *pluginapi.Completion) error { return c.SetFinishReason(0, "content_filter") },
		},
		{
			name: "replace text",
			edit: func(c *pluginapi.Completion) error { return c.ReplaceText(0, "[x]") },
			from: `[{"type":"output_text","text":"hello","annotations":[]},{"type":"output_text","text":" world","annotations":[]}]`,
			to:   `[{"type":"output_text","text":"[x]","annotations":[]}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, c := responsesCompletion(t)
			canonical := string(mustJSON(t, resp))
			want := canonical
			if tt.from != "" {
				require.Contains(t, canonical, tt.from)

				want = strings.Replace(canonical, tt.from, tt.to, 1)
			}
			err := tt.edit(c)
			require.NoError(t, err)

			applied, err := ApplyToResponsesResponse(resp, c)
			require.NoError(t, err)
			got := string(mustJSON(t, applied))
			assert.Equal(t, want, got)
			got = string(mustJSON(t, resp))
			assert.Equal(t, canonical, got)
		})
	}
}

func TestApplyToResponsesResponseReplaceWithoutMessageItem(t *testing.T) {
	resp := &core.ResponsesResponse{Status: "completed", Output: []core.ResponsesOutputItem{{ID: "fc", Type: "function_call", CallID: "c", Name: "f"}}}
	c, err := FromResponsesResponse(resp)
	require.NoError(t, err)
	err = c.ReplaceText(0, "blocked")
	require.NoError(t, err)

	applied, err := ApplyToResponsesResponse(resp, c)
	require.NoError(t, err)
	assert.Len(t, applied.Output, 2)
	assert.Equal(t, "message", applied.Output[1].Type)
	assert.Equal(t, "blocked", applied.Output[1].Content[0].Text)
	assert.True(t, strings.HasPrefix(applied.Output[1].ID, "msg_"))
}

func TestCompletionToResponsesResponse(t *testing.T) {
	c := pluginapi.Respond("nope").Response
	c.Choices[0].Message.Parts = append(c.Choices[0].Message.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}})
	resp := CompletionToResponsesResponse(c, "m")
	assert.True(t, strings.HasPrefix(resp.ID, "gomodel-plugin-"))
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "completed", resp.Status)
	assert.Equal(t, "m", resp.Model)
	assert.NotEqual(t, int64(0), resp.CreatedAt, "envelope = %+v", resp)
	assert.Len(t, resp.Output, 2)
	assert.Equal(t, "message", resp.Output[0].Type)
	assert.Equal(t, "nope", resp.Output[0].Content[0].Text)
	assert.Equal(t, "function_call", resp.Output[1].Type)
	assert.Equal(t, `{"a":1}`, resp.Output[1].Arguments)
	assert.NotNil(t, resp.Usage)
	assert.Equal(t, 0, resp.Usage.TotalTokens)
	_, err := json.Marshal(resp)
	assert.NoError(t, err)

	empty := CompletionToResponsesResponse(nil, "m")
	assert.Len(t, empty.Output, 1)
	assert.Equal(t, "message", empty.Output[0].Type)
}

func TestApplyToResponsesResponseToolArguments(t *testing.T) {
	resp, c := responsesCompletion(t)
	err := c.SetToolArguments(0, "c1", json.RawMessage(`{"a":2}`))
	require.NoError(t, err)

	applied, err := ApplyToResponsesResponse(resp, c)
	require.NoError(t, err)
	got := applied.Output[2].Arguments
	assert.Equal(t, `{"a":2}`, got)
	assert.Equal(t, `{"a":1}`, resp.Output[2].Arguments)

	// A replaced text keeps the argument edit, whichever came first.
	for _, first := range []string{"replace", "arguments"} {
		resp, c := responsesCompletion(t)
		edits := []func() error{
			func() error { return c.ReplaceText(0, "[x]") },
			func() error { return c.SetToolArguments(0, "c1", json.RawMessage(`{"a":2}`)) },
		}
		if first == "arguments" {
			edits[0], edits[1] = edits[1], edits[0]
		}
		for _, edit := range edits {
			err := edit()
			require.NoError(t, err)
		}
		applied, err := ApplyToResponsesResponse(resp, c)
		require.NoError(t, err)

		var call, msg *core.ResponsesOutputItem
		for i := range applied.Output {
			switch applied.Output[i].Type {
			case "function_call":
				call = &applied.Output[i]
			case "message":
				msg = &applied.Output[i]
			}
		}
		assert.NotNil(t, call)
		assert.Equal(t, `{"a":2}`, call.Arguments)
		assert.NotNil(t, msg)
		assert.Len(t, msg.Content, 1)
		assert.Equal(t, "[x]", msg.Content[0].Text, "%s first: %+v", first, applied.Output)
	}
}
