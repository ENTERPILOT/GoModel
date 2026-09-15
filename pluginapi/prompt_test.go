package pluginapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolPrompt() *Prompt {
	p := &Prompt{Messages: []Message{
		{ID: "m0", Role: RoleSystem, Parts: []Part{{Kind: PartText, Text: "be brief"}}},
		{ID: "m1", Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "weather?"}, {Kind: PartImage, URL: "https://x/y.png"}}},
		{ID: "m2", Role: RoleAssistant, Parts: []Part{{Kind: PartToolCall, ToolCall: &ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}}},
		{ID: "m3", Role: RoleTool, ToolCallID: "call_1", Parts: []Part{{Kind: PartToolResult, ToolResult: &ToolResult{CallID: "call_1", Parts: []Part{{Kind: PartText, Text: "rain"}}}}}},
		{ID: "m4", Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "thanks"}}},
	}}
	p.Reset()
	return p
}

func TestPromptViews(t *testing.T) {
	p := toolPrompt()
	last := p.LastUser()
	require.NotNil(t, last)
	assert.Equal(t, "m4", last.ID)
	got := p.Text()
	assert.Equal(t, "be brief\nweather?\nthanks", got)
	got = p.Text(RoleUser)
	assert.Equal(t, "weather?\nthanks", got)
	got = p.SystemText()
	assert.Equal(t, "be brief", got)

	calls := p.ToolCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "m2", calls[0].MessageID)
	assert.True(t, calls[0].HasResult)
	assert.Equal(t, "weather", calls[0].Call.Name)

	if got := len(p.NewSince(3)); got != 2 {
		t.Errorf("NewSince(3) len = %d, want 2", got)
	}
	if got := p.NewSince(99); got != nil {
		t.Errorf("NewSince(99) = %v, want nil", got)
	}
	assert.Nil(t, p.Message("nope"))
	assert.Equal(t, RoleTool, p.Message("m3").Role)
	assert.False(t, p.Changes().Dirty)
}

func TestPromptSetText(t *testing.T) {
	tests := []struct {
		name    string
		msgID   string
		partIdx int
		wantErr string
	}{
		{name: "text part", msgID: "m1", partIdx: 0},
		{name: "unknown message", msgID: "zz", partIdx: 0, wantErr: "unknown message"},
		{name: "part out of range", msgID: "m1", partIdx: 5, wantErr: "no part 5"},
		{name: "non-text part", msgID: "m1", partIdx: 1, wantErr: "not text"},
		{name: "tool call part", msgID: "m2", partIdx: 0, wantErr: "not text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := toolPrompt()
			err := p.SetText(tt.msgID, tt.partIdx, "new")
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				assert.False(t, p.Changes().Dirty)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, "new", p.Message(tt.msgID).Parts[tt.partIdx].Text)

			ch := p.Changes()
			assert.True(t, ch.Dirty)
			assert.Equal(t, ChangeEdited, ch.Messages[tt.msgID], "changes = %+v", ch)

		})
	}
}

func TestPromptToolEdits(t *testing.T) {
	p := toolPrompt()
	err := p.SetToolArguments("m2", "call_1", json.RawMessage(`{"city":"Bergen"}`))
	require.NoError(t, err)
	got := string(p.Message("m2").Parts[0].ToolCall.Arguments)
	assert.Equal(t, `{"city":"Bergen"}`, got)
	assert.Error(t, p.SetToolArguments("m2", "call_1", json.RawMessage(`{bad`)))
	assert.Error(t, p.SetToolArguments("m2", "call_9", json.RawMessage(`{}`)))
	err = p.SetToolResult("m3", "call_1", []Part{{Kind: PartText, Text: "[redacted]"}})
	require.NoError(t, err)
	got = p.Message("m3").Text()
	assert.Equal(t, "[redacted]", got)
	assert.Error(t, p.SetToolResult("m3", "call_9", nil))

	ch := p.Changes()
	assert.Equal(t, ChangeEdited, ch.Messages["m2"])
	assert.Equal(t, ChangeEdited, ch.Messages["m3"], "changes = %+v", ch.Messages)
}

func TestPromptInsertAppendRemove(t *testing.T) {
	p := toolPrompt()
	first := p.Insert(0, TextMessage(RoleSystem, "prefix"))
	last := p.Append(TextMessage(RoleUser, "suffix"))
	far := p.Insert(99, TextMessage(RoleUser, "clamped"))
	assert.Equal(t, "new-1", first)
	assert.Equal(t, "new-2", last)
	assert.Equal(t, "new-3", far)
	assert.Equal(t, first, p.Messages[0].ID)
	assert.Equal(t, far, p.Messages[len(p.Messages)-1].ID)
	assert.Equal(t, last, p.Messages[len(p.Messages)-2].ID)

	ch := p.Changes()
	assert.Equal(t, ChangeInserted, ch.Messages[first])
	assert.Equal(t, ChangeInserted, ch.Messages[last], "changes = %+v", ch.Messages)
	err := // Editing an inserted message keeps it inserted.
		p.SetText(first, 0, "prefix2")
	require.NoError(t, err)
	assert.Equal(t, ChangeInserted, p.Changes().Messages[first])
	err = // Removing an inserted message forgets it entirely.
		p.Remove(first)
	require.NoError(t, err)
	_, ok := p.Changes().Messages[first]
	assert.False(t, ok)
	assert.Nil(t, p.Message(first))
	err = // Removing an original records it.
		p.Remove("m0")
	require.NoError(t, err)
	assert.Equal(t, ChangeRemoved, p.Changes().Messages["m0"])
	assert.Nil(t, p.Message("m0"))
	assert.Error(t, p.Remove("m0"))
	assert.Error(t, p.Remove("zz"))
	err = p.Validate()
	assert.NoError(t, err)

	// IDs never collide with ones the host handed out.
	q := &Prompt{Messages: []Message{{ID: "new-1", Role: RoleUser}}}
	q.Reset()
	id := q.Append(TextMessage(RoleUser, "x"))
	assert.Equal(t, "new-2", id)
}

func TestPromptRemoveToolPairs(t *testing.T) {
	tests := []struct {
		name  string
		order []string
	}{
		{name: "call then result", order: []string{"m2", "m3"}},
		{name: "result then call", order: []string{"m3", "m2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := toolPrompt()
			err := p.Remove(tt.order[0])
			var dangling *DanglingToolError
			require.ErrorAs(t, err, &dangling)
			assert.Equal(t, tt.order[1], dangling.PartnerID)
			assert.Equal(t, "call_1", dangling.CallID, "dangling = %+v", dangling)
			assert.Nil(t, p.Message(tt.order[0]))
			verr := p.Validate()
			assert.ErrorAs(t, verr, &dangling)
			err = p.Remove(dangling.PartnerID)
			require.NoError(t, err)
			err = p.Validate()
			assert.NoError(t, err)

			ch := p.Changes()
			assert.Equal(t, ChangeRemoved, ch.Messages["m2"])
			assert.Equal(t, ChangeRemoved, ch.Messages["m3"], "changes = %+v", ch.Messages)
			assert.Empty(t, p.ToolCalls())

		})
	}
}

// Edits counts every edit call, repeats on one message included, so a host
// can tell whether a step edited anything after an earlier step already
// marked the prompt dirty.
func TestPromptChangesEdits(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(p *Prompt)
		want int
	}{
		{"untouched", func(*Prompt) {}, 0},
		{"one edit", func(p *Prompt) { _ = p.SetText("m1", 0, "a") }, 1},
		{"repeated edit of one message", func(p *Prompt) { _ = p.SetText("m1", 0, "a"); _ = p.SetText("m1", 0, "b") }, 2},
		{"param", func(p *Prompt) { p.SetParam("max_tokens", 1) }, 1},
		{"param set twice", func(p *Prompt) { p.SetParam("max_tokens", 1); p.SetParam("max_tokens", 2) }, 2},
		{"insert and remove", func(p *Prompt) {
			p.Insert(0, Message{Role: RoleSystem, Parts: []Part{{Kind: PartText, Text: "x"}}})
			_ = p.Remove("m4")
		}, 2},
		{"rejected edits", func(p *Prompt) {
			_ = p.SetText("nope", 0, "x")
			_ = p.SetText("m1", 9, "x")
			_ = p.SetText("m2", 0, "x") // a tool call, not text
			_ = p.Remove("nope")
		}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := toolPrompt()
			tt.edit(p)
			ch := p.Changes()
			require.Equal(t, tt.want, ch.Edits)
			require.Equal(t, tt.want > 0, ch.Dirty)

			// Changes() is a copy: later edits do not move it, Reset clears it.
			_ = p.SetText("m0", 0, "later")
			assert.Equal(t, tt.want, ch.Edits)
			assert.Equal(t, tt.want+1, p.Changes().Edits)

			p.Reset()
			got := p.Changes()
			assert.Equal(t, 0, got.Edits)
			assert.False(t, got.Dirty, "Changes() after Reset = %+v", got)

		})
	}
}

// Clone is independent of the original: edits on either side, including
// the change tracking, stay on their side, and the copy keeps generating
// fresh IDs.
func TestPromptClone(t *testing.T) {
	p := toolPrompt()
	_ = p.SetText("m1", 0, "first")
	c := p.Clone()
	require.Equal(t, p.Text(), c.Text())
	require.Equal(t, 1, c.Changes().Edits)
	require.Equal(t, ChangeEdited, c.Changes().Messages["m1"], "clone differs from the original: %q, %+v", c.Text(), c.Changes())

	_ = p.SetText("m1", 0, "second")
	_ = p.SetToolArguments("m2", "call_1", json.RawMessage(`{"city":"Rome"}`))
	_ = p.Remove("m4")
	assert.Equal(t, "first", c.Messages[1].Text())
	require.Len(t, c.Messages, 5)
	assert.Equal(t, 1, c.Changes().Edits)
	got := string(c.Messages[2].Parts[0].ToolCall.Arguments)
	assert.Equal(t, `{"city":"Oslo"}`, got)
	id := c.Insert(0, TextMessage(RoleSystem, "x"))
	assert.Equal(t, "new-1", id)
	assert.Len(t, p.Messages, 4)
	assert.Nil(t, p.Message(id))
	assert.Equal(t, "new-1", p.Clone().Insert(0, TextMessage(RoleSystem, "y")))

	if got := c.Changes(); got.Messages["m4"] != "" || len(c.removed) != 0 {
		t.Errorf("removal on the original reached the clone: %+v", got)
	}
	assert.Nil(t, (*Prompt)(nil).Clone())

	// Tool-call arguments are their own bytes on each side.
	c = p.Clone()
	c.Messages[2].Parts[0].ToolCall.Arguments[2] = 'X'
	got = string(p.Messages[2].Parts[0].ToolCall.Arguments)
	assert.Equal(t, `{"city":"Rome"}`, got)

	// The extra parameters are copied, nested values included.
	p.Params.Extra = map[string]any{"metadata": map[string]any{"team": "a"}}
	c = p.Clone()
	p.Params.Extra["metadata"].(map[string]any)["team"] = "b"
	p.Params.Extra["new"] = true
	if got := c.Params.Extra["metadata"].(map[string]any)["team"]; got != "a" || c.Params.Extra["new"] != nil {
		t.Errorf("clone shares the original's extra parameters: %v", c.Params.Extra)
	}

	// Object-valued parameters are copied too, not shared.
	p.Params.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": "weather"}}
	c = p.Clone()
	p.Params.ToolChoice.(map[string]any)["function"].(map[string]any)["name"] = "changed"
	if got := c.Params.ToolChoice.(map[string]any)["function"].(map[string]any)["name"]; got != "weather" {
		t.Errorf("clone tool_choice shares the original's map: %v", got)
	}
}

func TestPromptSetParam(t *testing.T) {
	p := toolPrompt()
	p.SetParam("max_tokens", 42)
	p.SetParam("temperature", 0.5)
	p.SetParam("top_p", json.Number("0.9"))
	p.SetParam("user", "alice")
	require.NotNil(t, p.Params.MaxTokens)
	assert.Equal(t, 42, *p.Params.MaxTokens)
	require.NotNil(t, p.Params.Temperature)
	assert.Equal(t, 0.5, *p.Params.Temperature)
	require.NotNil(t, p.Params.TopP)
	assert.Equal(t, 0.9, *p.Params.TopP)

	ch := p.Changes()
	assert.True(t, ch.Dirty)
	require.Len(t, ch.Params, 4)
	assert.Equal(t, "alice", ch.Params["user"], "changes = %+v", ch)

	p.Reset()
	assert.False(t, p.Changes().Dirty)

	// Changes() returns a copy.
	p.SetParam("x", 1)
	ch = p.Changes()
	ch.Params["y"] = 2
	_, ok := p.Changes().Params["y"]
	assert.False(t, ok)
}

func TestValuesNilSafe(t *testing.T) {
	var v Values
	_, ok := v.Get("k")
	assert.False(t, ok)

	v = Values{}
	v.Set("k", 1)
	got, ok := v.Get("k")
	assert.True(t, ok)
	assert.Equal(t, 1, got)
}
