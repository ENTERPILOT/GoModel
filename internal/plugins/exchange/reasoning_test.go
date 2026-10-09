package exchange

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// A replayed assistant turn's reasoning is a reasoning part of the prompt,
// written back to the member it came in.
func TestChatRequestReasoning(t *testing.T) {
	for _, member := range []string{"reasoning_content", "reasoning"} {
		t.Run(member, func(t *testing.T) {
			req := decodeChat(t, `{"model":"m","messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","`+member+`":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"c1","content":"ok"}
			]}`)
			p, err := FromChatRequest(req)
			require.NoError(t, err)
			targets := p.ReasoningTargets()
			require.Len(t, targets, 1)
			assert.Equal(t, pluginapi.TextTarget{MessageID: "m1", Role: pluginapi.RoleAssistant, Part: 0, Reasoning: true, Text: "think"}, targets[0])
			assert.Equal(t, "hello", p.Message("m1").Text())

			// Unedited, the message is copied as it was.
			applied, err := ApplyToChatRequest(req, p)
			require.NoError(t, err)
			assert.Equal(t, messageJSON(t, req.Messages[1]), messageJSON(t, applied.Messages[1]))

			err = p.SetTargetText(targets[0], "thought")
			require.NoError(t, err)
			err = p.SetText("m1", 1, "hey")
			require.NoError(t, err)
			applied, err = ApplyToChatRequest(req, p)
			require.NoError(t, err)
			got := messageJSON(t, applied.Messages[1])
			assert.Equal(t, `{"role":"assistant","content":"hey","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}],"`+member+`":"thought"}`, got)
			assert.Contains(t, messageJSON(t, req.Messages[1]), `"think"`, "original mutated")
		})
	}
}

func TestChatResponseVendorReasoningMember(t *testing.T) {
	var resp core.ChatResponse
	err := json.Unmarshal([]byte(`{"id":"r","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi","reasoning":"think"},"finish_reason":"stop"}]}`), &resp)
	require.NoError(t, err)
	c, err := FromChatResponse(&resp)
	require.NoError(t, err)
	targets := c.ReasoningTargets()
	require.Len(t, targets, 1)
	err = c.SetTargetText(targets[0], "thought")
	require.NoError(t, err)

	applied, err := ApplyToChatResponse(&resp, c)
	require.NoError(t, err)
	got := string(mustJSON(t, applied.Choices[0].Message))
	assert.Contains(t, got, `"reasoning":"thought"`)
	assert.NotContains(t, got, "reasoning_content")
}

// A message carrying both reasoning members exposes the text of each, so
// neither reaches the provider (or the client) unedited. Equal text is one
// part, written back to both.
func TestChatReasoningBothMembers(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		reasoning string
		want      []string
	}{
		{"different", "think", "ponder", []string{"think", "ponder"}},
		{"equal", "think", "think", []string{"think"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := decodeChat(t, `{"model":"m","messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","reasoning_content":"`+tt.content+`","reasoning":"`+tt.reasoning+`"}
			]}`)
			p, err := FromChatRequest(req)
			require.NoError(t, err)
			var texts []string
			for _, target := range p.ReasoningTargets() {
				texts = append(texts, target.Text)
				err := p.SetTargetText(target, target.Text+"!")
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, texts)
			applied, err := ApplyToChatRequest(req, p)
			require.NoError(t, err)
			got := messageJSON(t, applied.Messages[1])
			assert.Contains(t, got, `"reasoning_content":"`+tt.content+`!"`)
			assert.Contains(t, got, `"reasoning":"`+tt.reasoning+`!"`)

			var resp core.ChatResponse
			err = json.Unmarshal([]byte(`{"id":"r","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi","reasoning_content":"`+tt.content+`","reasoning":"`+tt.reasoning+`"},"finish_reason":"stop"}]}`), &resp)
			require.NoError(t, err)
			c, err := FromChatResponse(&resp)
			require.NoError(t, err)
			texts = nil
			for _, target := range c.ReasoningTargets() {
				texts = append(texts, target.Text)
				err := c.SetTargetText(target, target.Text+"!")
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, texts)
			out, err := ApplyToChatResponse(&resp, c)
			require.NoError(t, err)
			got = string(mustJSON(t, out.Choices[0].Message))
			assert.Contains(t, got, `"reasoning_content":"`+tt.content+`!"`)
			assert.Contains(t, got, `"reasoning":"`+tt.reasoning+`!"`)
		})
	}
}

// A reasoning item lists its reasoning_text content and its summary entries
// as parts of their own, each written back in place.
func TestResponsesReasoningSegments(t *testing.T) {
	var resp core.ResponsesResponse
	err := json.Unmarshal([]byte(`{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[
		{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"raw"}],"summary":[{"type":"summary_text","text":"one"},{"type":"summary_text","text":"two"}]},
		{"id":"rs_2","type":"reasoning","summary":[]},
		{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}
	]}`), &resp)
	require.NoError(t, err)
	c, err := FromResponsesResponse(&resp)
	require.NoError(t, err)
	var texts []string
	for _, target := range c.ReasoningTargets() {
		texts = append(texts, target.Text)
		err := c.SetTargetText(target, target.Text+"!")
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"raw", "one", "two", ""}, texts)

	applied, err := ApplyToResponsesResponse(&resp, c)
	require.NoError(t, err)
	got := string(mustJSON(t, applied.Output[0]))
	assert.Contains(t, got, `"content":[{"type":"reasoning_text","text":"raw!"}]`)
	assert.Contains(t, got, `"summary":[{"type":"summary_text","text":"one!"},{"type":"summary_text","text":"two!"}]`)
	assert.Equal(t, string(mustJSON(t, resp.Output[1])), string(mustJSON(t, applied.Output[1])), "item without text changed")
	assert.Contains(t, string(mustJSON(t, resp.Output[0])), `"text":"raw"`, "original mutated")
}
