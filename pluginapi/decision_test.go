package pluginapi

import "testing"

func TestDecisions(t *testing.T) {
	tests := []struct {
		name   string
		d      Decision
		action Action
		blocks bool
	}{
		{name: "allow", d: Allow(), action: ActionAllow},
		{name: "block", d: Block(446, "policy", "no"), action: ActionBlock, blocks: true},
		{name: "respond", d: Respond("I can't help with that"), action: ActionRespond, blocks: true},
		{name: "warn", d: Warn("pii", "found email", map[string]int{"count": 1}), action: ActionWarn},
		{name: "zero value", d: Decision{}, action: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.d.Action != tt.action {
				t.Errorf("action = %q, want %q", tt.d.Action, tt.action)
			}
			if tt.d.Blocks() != tt.blocks {
				t.Errorf("Blocks() = %v, want %v", tt.d.Blocks(), tt.blocks)
			}
		})
	}

	b := Block(446, "policy", "no")
	if b.Status != 446 || b.Code != "policy" || b.Message != "no" {
		t.Errorf("Block fields = %+v", b)
	}
	r := Respond("nope")
	if r.Response == nil || len(r.Response.Choices) != 1 {
		t.Fatal("Respond must build a one-choice completion")
	}
	ch := r.Response.Choices[0]
	if ch.Index != 0 || ch.FinishReason != "stop" || ch.Message.Role != RoleAssistant || ch.Message.Text() != "nope" {
		t.Errorf("Respond choice = %+v", ch)
	}
	w := Warn("pii", "found", 3)
	if w.Code != "pii" || w.Message != "found" || w.Detail != 3 {
		t.Errorf("Warn fields = %+v", w)
	}
}
