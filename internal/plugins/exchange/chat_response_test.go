package exchange

import (
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"

	"github.com/goccy/go-json"
)

const chatResponseFixture = `{"id":"r1","object":"chat.completion","model":"m","provider":"p","choices":[{"index":0,"message":{"role":"assistant","content":"hello world","reasoning_content":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls","native_finish":"x"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}},"created":1}`

func chatCompletion(t *testing.T) (*core.ChatResponse, *pluginapi.Completion) {
	t.Helper()
	var resp core.ChatResponse
	if err := json.Unmarshal([]byte(chatResponseFixture), &resp); err != nil {
		t.Fatal(err)
	}
	c, err := FromChatResponse(&resp)
	if err != nil {
		t.Fatal(err)
	}
	return &resp, c
}

func TestFromChatResponse(t *testing.T) {
	_, c := chatCompletion(t)
	if c.ID != "r1" || c.Model != "m" || len(c.Choices) != 1 {
		t.Fatalf("completion = %+v", c)
	}
	parts := c.Choices[0].Message.Parts
	if len(parts) != 3 || parts[0].Kind != pluginapi.PartReasoning || parts[0].Text != "think" || parts[1].Text != "hello world" || parts[2].Kind != pluginapi.PartToolCall || parts[2].ToolCall.ID != "c1" {
		t.Errorf("parts = %+v", parts)
	}
	if c.Choices[0].FinishReason != "tool_calls" || c.Choices[0].Message.ID != "choice:0" {
		t.Errorf("choice = %+v", c.Choices[0])
	}
	if c.Usage != (pluginapi.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CachedInputTokens: 4}) {
		t.Errorf("usage = %+v", c.Usage)
	}
	if c.Text(0) != "hello world" || c.Changes().Dirty {
		t.Error("text or clean state wrong")
	}
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
				if !strings.Contains(canonical, tt.from) {
					t.Fatalf("fixture lacks %q", tt.from)
				}
				want = strings.Replace(canonical, tt.from, tt.to, 1)
			}
			if err := tt.edit(c); err != nil {
				t.Fatal(err)
			}
			applied, err := ApplyToChatResponse(resp, c)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(mustJSON(t, applied)); got != want {
				t.Errorf("\n got: %s\nwant: %s", got, want)
			}
			// The original is never mutated.
			if got := string(mustJSON(t, resp)); got != canonical {
				t.Errorf("original mutated: %s", got)
			}
		})
	}
}

func TestCompletionToChatResponse(t *testing.T) {
	resp := CompletionToChatResponse(pluginapi.Respond("nope").Response, "m")
	if !strings.HasPrefix(resp.ID, "gomodel-plugin-") || resp.Object != "chat.completion" || resp.Model != "m" || resp.Created == 0 {
		t.Errorf("envelope = %+v", resp)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "nope" || resp.Choices[0].FinishReason != "stop" || resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("choice = %+v", resp.Choices)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Error("usage must be zero")
	}
	if _, err := json.Marshal(resp); err != nil {
		t.Errorf("must encode: %v", err)
	}
	empty := CompletionToChatResponse(nil, "m")
	if len(empty.Choices) != 1 {
		t.Error("nil completion must still yield one choice")
	}
}
