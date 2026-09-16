package pluginapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamState(t *testing.T) {
	var nilState *StreamState
	assert.Empty(t, nilState.Text(0))
	assert.Equal(t, 0, nilState.Events())

	s := &StreamState{}
	s.Append(&StreamEvent{Seq: 1, Kind: EventTextDelta, Choice: 0, Text: "hel"})
	s.Append(&StreamEvent{Seq: 2, Kind: EventToolCallDelta, Choice: 0, Text: `{"a"`})
	s.Append(&StreamEvent{Seq: 3, Kind: EventTextDelta, Choice: 0, Text: "lo"})
	s.Append(&StreamEvent{Seq: 4, Kind: EventTextDelta, Choice: 1, Text: "other"})
	s.Append(nil)
	got := s.Text(0)
	assert.Equal(t, "hello", got)
	got = s.Text(1)
	assert.Equal(t, "other", got)
	got = s.Text(2)
	assert.Empty(t, got)

	if got := s.Events(); got != 4 {
		t.Errorf("Events = %d, want 4", got)
	}
}

func TestStreamDecisions(t *testing.T) {
	assert.Equal(t, StreamPass, Pass().Action)
	assert.Equal(t, StreamDrop, Drop().Action)
	r := Replace("x")
	assert.Equal(t, StreamReplace, r.Action)
	assert.Equal(t, "x", r.Text, "Replace = %+v", r)

	term := Terminate(Block(0, "c", "m"))
	assert.Equal(t, StreamTerminate, term.Action)
	require.NotNil(t, term.Terminate)
	assert.Equal(t, "c", term.Terminate.Code, "Terminate = %+v", term)
}

func TestMessageTextAndRoute(t *testing.T) {
	m := Message{Role: RoleTool, Parts: []Part{
		{Kind: PartText, Text: "a"},
		{Kind: PartToolResult, ToolResult: &ToolResult{Parts: []Part{{Kind: PartText, Text: "b"}, {Kind: PartImage}}}},
		{Kind: PartToolResult},
		{Kind: PartImage},
	}}
	got := m.Text()
	assert.Equal(t, "ab", got)
	got = (RouteTarget{Provider: "openai", Model: "gpt-4o"}).Qualified()
	assert.Equal(t, "openai/gpt-4o", got)
}

func TestStreamStateReplaceTail(t *testing.T) {
	s := &StreamState{}
	s.Append(&StreamEvent{Seq: 1, Kind: EventTextDelta, Text: "my key sec"})
	// Lookbehind showed "sec" again in front of "ret ok"; the plugin replaced
	// the whole window.
	s.ReplaceTail(&StreamEvent{Seq: 2, Kind: EventTextDelta, Text: "secret ok", Overlap: 3}, 3, "[x] ok")
	got := s.Text(0)
	assert.Equal(t, "my key [x] ok", got)

	s.ReplaceTail(&StreamEvent{Seq: 3, Kind: EventTextDelta, Text: "ok bye"}, 2, "")
	got = s.Text(0)
	assert.Equal(t, "my key [x] ", got)

	s.ReplaceTail(&StreamEvent{Seq: 4, Kind: EventReasoningDelta, Text: "hmm"}, 0, "")
	if got, n := s.Text(0), s.Events(); got != "my key [x] " || n != 4 {
		t.Errorf("reasoning delta changed text %q, events = %d", got, n)
	}
}
