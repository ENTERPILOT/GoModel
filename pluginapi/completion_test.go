package pluginapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleCompletion() *Completion {
	c := &Completion{Choices: []Choice{
		{Index: 0, FinishReason: "stop", Message: Message{Role: RoleAssistant, Parts: []Part{
			{Kind: PartReasoning, Text: "thinking"},
			{Kind: PartText, Text: "hello "},
			{Kind: PartText, Text: "world"},
			{Kind: PartToolCall, ToolCall: &ToolCall{ID: "c1", Name: "f"}},
		}}},
		{Index: 1, FinishReason: "stop", Message: TextMessage(RoleAssistant, "second")},
	}}
	c.Reset()
	return c
}

func TestCompletionText(t *testing.T) {
	c := sampleCompletion()
	got := c.Text(0)
	assert.Equal(t, "hello world", got)
	got = c.Text(7)
	assert.Empty(t, got)
}

func TestCompletionEdits(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(c *Completion) error
		wantErr bool
		wantKey string
		want    ChangeKind
		check   func(t *testing.T, c *Completion)
	}{
		{
			name:    "set text",
			edit:    func(c *Completion) error { return c.SetText(0, 1, "bye ") },
			wantKey: "choice:0", want: ChangeEdited,
			check: func(t *testing.T, c *Completion) {
				assert.Equal(t, "bye world", c.Text(0))

			},
		},
		{name: "set text on reasoning part", edit: func(c *Completion) error { return c.SetText(0, 0, "x") }, wantErr: true},
		{name: "set text bad choice", edit: func(c *Completion) error { return c.SetText(5, 0, "x") }, wantErr: true},
		{name: "set text bad part", edit: func(c *Completion) error { return c.SetText(1, 3, "x") }, wantErr: true},
		{
			name:    "finish reason",
			edit:    func(c *Completion) error { return c.SetFinishReason(1, "content_filter") },
			wantKey: "choice:1", want: ChangeEdited,
			check: func(t *testing.T, c *Completion) {
				assert.Equal(t, "content_filter", c.Choices[1].FinishReason)

			},
		},
		{name: "finish reason bad choice", edit: func(c *Completion) error { return c.SetFinishReason(9, "stop") }, wantErr: true},
		{
			name:    "replace text keeps non-text parts",
			edit:    func(c *Completion) error { return c.ReplaceText(0, "[redacted]") },
			wantKey: "choice:0", want: ChangeReplaced,
			check: func(t *testing.T, c *Completion) {
				parts := c.Choices[0].Message.Parts
				require.Len(t, parts, 3)
				assert.Equal(t, PartReasoning, parts[0].Kind)
				assert.Equal(t, "[redacted]", parts[1].Text)
				assert.Equal(t, PartToolCall, parts[2].Kind)

			},
		},
		{
			name: "replace text with no text parts prepends",
			edit: func(c *Completion) error {
				c.Choices[1].Message.Parts = []Part{{Kind: PartToolCall, ToolCall: &ToolCall{ID: "z"}}}
				return c.ReplaceText(1, "answer")
			},
			wantKey: "choice:1", want: ChangeReplaced,
			check: func(t *testing.T, c *Completion) {
				parts := c.Choices[1].Message.Parts
				require.Len(t, parts, 2)
				assert.Equal(t, "answer", parts[0].Text)
				assert.Equal(t, PartToolCall, parts[1].Kind)

			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := sampleCompletion()
			err := tt.edit(c)
			if tt.wantErr {
				require.Error(t, err)
				assert.False(t, c.Changes().Dirty)

				return
			}
			require.NoError(t, err)

			ch := c.Changes()
			assert.True(t, ch.Dirty)
			assert.Equal(t, tt.want, ch.Messages[tt.wantKey], "changes = %+v", ch)

			tt.check(t, c)
		})
	}
}

func TestCompletionReplaceThenEditStaysReplaced(t *testing.T) {
	c := sampleCompletion()
	err := c.ReplaceText(0, "a")
	require.NoError(t, err)
	err = c.SetText(0, 1, "b")
	require.NoError(t, err)
	assert.Equal(t, ChangeReplaced, c.Changes().Messages["choice:0"])

	c.Reset()
	assert.False(t, c.Changes().Dirty)
}
