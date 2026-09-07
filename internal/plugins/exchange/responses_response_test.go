package exchange

import (
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"

	"github.com/goccy/go-json"
)

const responsesResponseFixture = `{"id":"resp_1","object":"response","created_at":1,"model":"m","provider":"p","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]},{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]},{"type":"output_text","text":" world","annotations":[]}]},{"id":"fc_1","type":"function_call","call_id":"c1","name":"f","arguments":"{\"a\":1}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`

func responsesCompletion(t *testing.T) (*core.ResponsesResponse, *pluginapi.Completion) {
	t.Helper()
	var resp core.ResponsesResponse
	if err := json.Unmarshal([]byte(responsesResponseFixture), &resp); err != nil {
		t.Fatal(err)
	}
	c, err := FromResponsesResponse(&resp)
	if err != nil {
		t.Fatal(err)
	}
	return &resp, c
}

func TestFromResponsesResponse(t *testing.T) {
	_, c := responsesCompletion(t)
	if len(c.Choices) != 1 || c.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("choices = %+v", c.Choices)
	}
	parts := c.Choices[0].Message.Parts
	if len(parts) != 4 || parts[0].Kind != pluginapi.PartReasoning || parts[0].Text != "thinking" || parts[1].Text != "hello" || parts[2].Text != " world" || parts[3].Kind != pluginapi.PartToolCall || string(parts[3].ToolCall.Arguments) != `{"a":1}` {
		t.Errorf("parts = %+v", parts)
	}
	if c.Usage != (pluginapi.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}) {
		t.Errorf("usage = %+v", c.Usage)
	}
	if c.Text(0) != "hello world" {
		t.Errorf("text = %q", c.Text(0))
	}

	plain, err := FromResponsesResponse(&core.ResponsesResponse{Status: "incomplete"})
	if err != nil || plain.Choices[0].FinishReason != "length" {
		t.Errorf("incomplete finish reason = %+v, %v", plain.Choices, err)
	}
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
				if !strings.Contains(canonical, tt.from) {
					t.Fatalf("fixture lacks %q", tt.from)
				}
				want = strings.Replace(canonical, tt.from, tt.to, 1)
			}
			if err := tt.edit(c); err != nil {
				t.Fatal(err)
			}
			applied, err := ApplyToResponsesResponse(resp, c)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(mustJSON(t, applied)); got != want {
				t.Errorf("\n got: %s\nwant: %s", got, want)
			}
			if got := string(mustJSON(t, resp)); got != canonical {
				t.Errorf("original mutated: %s", got)
			}
		})
	}
}

func TestApplyToResponsesResponseReplaceWithoutMessageItem(t *testing.T) {
	resp := &core.ResponsesResponse{Status: "completed", Output: []core.ResponsesOutputItem{{ID: "fc", Type: "function_call", CallID: "c", Name: "f"}}}
	c, err := FromResponsesResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReplaceText(0, "blocked"); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyToResponsesResponse(resp, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Output) != 2 || applied.Output[1].Type != "message" || applied.Output[1].Content[0].Text != "blocked" || !strings.HasPrefix(applied.Output[1].ID, "msg_") {
		t.Errorf("output = %+v", applied.Output)
	}
}

func TestCompletionToResponsesResponse(t *testing.T) {
	c := pluginapi.Respond("nope").Response
	c.Choices[0].Message.Parts = append(c.Choices[0].Message.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}})
	resp := CompletionToResponsesResponse(c, "m")
	if !strings.HasPrefix(resp.ID, "gomodel-plugin-") || resp.Object != "response" || resp.Status != "completed" || resp.Model != "m" || resp.CreatedAt == 0 {
		t.Errorf("envelope = %+v", resp)
	}
	if len(resp.Output) != 2 || resp.Output[0].Type != "message" || resp.Output[0].Content[0].Text != "nope" || resp.Output[1].Type != "function_call" || resp.Output[1].Arguments != `{"a":1}` {
		t.Errorf("output = %+v", resp.Output)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 0 {
		t.Error("usage must be zero")
	}
	if _, err := json.Marshal(resp); err != nil {
		t.Errorf("must encode: %v", err)
	}
	empty := CompletionToResponsesResponse(nil, "m")
	if len(empty.Output) != 1 || empty.Output[0].Type != "message" {
		t.Errorf("nil completion output = %+v", empty.Output)
	}
}
