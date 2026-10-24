package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// fakeDecider records the Decisions body and context it is sent and answers
// with a fixed body.
type fakeDecider struct {
	answer    string
	err       error
	decisions map[string]bool
	body      []byte
	ctx       context.Context
}

func (d *fakeDecider) Decide(ctx context.Context, body []byte) ([]byte, error) {
	d.ctx, d.body = ctx, body
	return []byte(d.answer), d.err
}

func (d *fakeDecider) DecisionModel(model string) bool { return d.decisions[model] }

// A decision request reaches the Decider as an OpenAI Decisions body, with
// origin "plugin" and the instance's user path, and the answer comes back
// decoded.
func TestHostDecisions(t *testing.T) {
	decider := &fakeDecider{
		answer:    `{"model":"jev-1.13.0","answers":[{"name":"unsafe","type":"predicate","probability":0.97}]}`,
		decisions: map[string]bool{"jev/jev-latest": true},
	}
	h := NewHost(HostDeps{Decisions: decider}, HostInfo{PluginName: "llm_judge", InstanceName: "policy", UserPath: "/team"})
	dh, ok := h.(pluginapi.DecisionsHost)
	require.True(t, ok)

	result, err := dh.Decisions().Decide(context.Background(), pluginapi.DecisionRequest{
		Model:     " jev/jev-latest ",
		Input:     []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "hello")},
		Questions: []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Name: "unsafe", Instructions: "Unsafe?"}},
	})
	require.NoError(t, err)
	require.Len(t, result.Answers, 1)
	assert.InDelta(t, 0.97, result.Answers[0].Probability, 1e-9)
	assert.JSONEq(t, `{"model":"jev/jev-latest","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"questions":[{"type":"predicate","name":"unsafe","instructions":"Unsafe?"}]}`, string(decider.body))
	assert.Equal(t, core.RequestOriginPlugin, core.GetRequestOrigin(decider.ctx))
	assert.Equal(t, "/team/guardrails/policy", core.UserPathFromContext(decider.ctx))

	assert.True(t, dh.Decisions().DecisionModel("jev/jev-latest"))
	assert.False(t, dh.Decisions().DecisionModel("openai/gpt-4o-mini"))
}

func TestHostDecisionsErrors(t *testing.T) {
	question := []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Instructions: "?"}}
	input := []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "x")}

	unwired := NewHost(HostDeps{}, HostInfo{PluginName: "p", InstanceName: "i"}).(pluginapi.DecisionsHost).Decisions()
	_, err := unwired.Decide(context.Background(), pluginapi.DecisionRequest{Model: "m", Input: input, Questions: question})
	require.ErrorIs(t, err, ErrDecisionsUnavailable)
	assert.False(t, unwired.DecisionModel("m"))

	upstream := errors.New("upstream failed")
	decisions := NewHost(HostDeps{Decisions: &fakeDecider{err: upstream}}, HostInfo{PluginName: "p", InstanceName: "i"}).(pluginapi.DecisionsHost).Decisions()
	_, err = decisions.Decide(context.Background(), pluginapi.DecisionRequest{Model: " ", Input: input, Questions: question})
	require.ErrorContains(t, err, "model is required")
	_, err = decisions.Decide(context.Background(), pluginapi.DecisionRequest{Model: "m", Input: input})
	require.ErrorContains(t, err, "at least one question")
	_, err = decisions.Decide(context.Background(), pluginapi.DecisionRequest{Model: "m", Input: input, Questions: question})
	require.ErrorIs(t, err, upstream)
}
