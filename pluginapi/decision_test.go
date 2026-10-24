package pluginapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			assert.Equal(t, tt.action, tt.d.Action)
			assert.Equal(t, tt.blocks, tt.d.Blocks())
		})
	}

	b := Block(446, "policy", "no")
	assert.Equal(t, 446, b.Status)
	assert.Equal(t, "policy", b.Code)
	assert.Equal(t, "no", b.Message, "Block fields = %+v", b)

	r := Respond("nope")
	require.NotNil(t, r.Response)
	require.Len(t, r.Response.Choices, 1)

	ch := r.Response.Choices[0]
	assert.Equal(t, 0, ch.Index)
	assert.Equal(t, "stop", ch.FinishReason)
	assert.Equal(t, RoleAssistant, ch.Message.Role)
	assert.Equal(t, "nope", ch.Message.Text(), "Respond choice = %+v", ch)

	w := Warn("pii", "found", 3)
	assert.Equal(t, "pii", w.Code)
	assert.Equal(t, "found", w.Message)
	assert.Equal(t, 3, w.Detail, "Warn fields = %+v", w)
}
