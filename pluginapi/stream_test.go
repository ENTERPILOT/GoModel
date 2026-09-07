package pluginapi

import "testing"

func TestStreamState(t *testing.T) {
	var nilState *StreamState
	if nilState.Text(0) != "" || nilState.Events() != 0 {
		t.Error("nil StreamState must be readable")
	}
	s := &StreamState{}
	s.Append(&StreamEvent{Seq: 1, Kind: EventTextDelta, Choice: 0, Text: "hel"})
	s.Append(&StreamEvent{Seq: 2, Kind: EventToolCallDelta, Choice: 0, Text: `{"a"`})
	s.Append(&StreamEvent{Seq: 3, Kind: EventTextDelta, Choice: 0, Text: "lo"})
	s.Append(&StreamEvent{Seq: 4, Kind: EventTextDelta, Choice: 1, Text: "other"})
	s.Append(nil)
	if got := s.Text(0); got != "hello" {
		t.Errorf("Text(0) = %q", got)
	}
	if got := s.Text(1); got != "other" {
		t.Errorf("Text(1) = %q", got)
	}
	if got := s.Text(2); got != "" {
		t.Errorf("Text(2) = %q", got)
	}
	if got := s.Events(); got != 4 {
		t.Errorf("Events = %d, want 4", got)
	}
}

func TestStreamDecisions(t *testing.T) {
	if Pass().Action != StreamPass || Drop().Action != StreamDrop {
		t.Error("Pass/Drop actions wrong")
	}
	if r := Replace("x"); r.Action != StreamReplace || r.Text != "x" {
		t.Errorf("Replace = %+v", r)
	}
	term := Terminate(Block(0, "c", "m"))
	if term.Action != StreamTerminate || term.Terminate == nil || term.Terminate.Code != "c" {
		t.Errorf("Terminate = %+v", term)
	}
}

func TestMessageTextAndRoute(t *testing.T) {
	m := Message{Role: RoleTool, Parts: []Part{
		{Kind: PartText, Text: "a"},
		{Kind: PartToolResult, ToolResult: &ToolResult{Parts: []Part{{Kind: PartText, Text: "b"}, {Kind: PartImage}}}},
		{Kind: PartToolResult},
		{Kind: PartImage},
	}}
	if got := m.Text(); got != "ab" {
		t.Errorf("Text = %q", got)
	}
	if got := (RouteTarget{Provider: "openai", Model: "gpt-4o"}).Qualified(); got != "openai/gpt-4o" {
		t.Errorf("Qualified = %q", got)
	}
}

func TestStreamStateReplaceTail(t *testing.T) {
	s := &StreamState{}
	s.Append(&StreamEvent{Seq: 1, Kind: EventTextDelta, Text: "my key sec"})
	// Lookbehind showed "sec" again in front of "ret ok"; the plugin replaced
	// the whole window.
	s.ReplaceTail(&StreamEvent{Seq: 2, Kind: EventTextDelta, Text: "secret ok", Overlap: 3}, 3, "[x] ok")
	if got := s.Text(0); got != "my key [x] ok" {
		t.Errorf("Text(0) = %q", got)
	}
	s.ReplaceTail(&StreamEvent{Seq: 3, Kind: EventTextDelta, Text: "ok bye"}, 2, "")
	if got := s.Text(0); got != "my key [x] " {
		t.Errorf("after drop Text(0) = %q", got)
	}
	s.ReplaceTail(&StreamEvent{Seq: 4, Kind: EventReasoningDelta, Text: "hmm"}, 0, "")
	if got, n := s.Text(0), s.Events(); got != "my key [x] " || n != 4 {
		t.Errorf("reasoning delta changed text %q, events = %d", got, n)
	}
}
