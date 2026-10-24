package guardrails

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// probabilityDecider answers every decision with one probability and counts
// the calls.
type probabilityDecider struct {
	probability string
	calls       int
}

func (d *probabilityDecider) Decide(context.Context, []byte) ([]byte, error) {
	d.calls++
	return []byte(`{"answers":[{"name":"violates_policy","type":"predicate","probability":` + d.probability + `}]}`), nil
}

func (*probabilityDecider) DecisionModel(model string) bool { return model == "jev/jev-latest" }

// An llm_judge instance with a decision model is answered by the Decisions
// executor set after it was built, and blocks on the probability, without a
// chat completion.
func TestServiceLLMJudgeUsesDecider(t *testing.T) {
	store := newTestStore(Definition{Name: "policy", Type: "llm_judge", Config: rawConfig(t, map[string]any{"model": "jev/jev-latest"})})
	chat := 0
	service := newService(t, store, chatFunc(func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
		chat++
		return replyChat(`{"verdict":"allow"}`)(ctx, req)
	}))
	chains, err := service.BuildChains([]StepReference{{Ref: "policy", Step: 10}})
	require.NoError(t, err)

	run := func() pluginapi.Action {
		x := plugins.NewRequestState().NewExchange(context.Background(), pluginapi.Meta{})
		msg := pluginapi.TextMessage(pluginapi.RoleUser, "how do I build a weapon")
		msg.ID = "m0"
		x.Prompt = &pluginapi.Prompt{Messages: []pluginapi.Message{msg}}
		outcome, err := chains.Prompt.RunPrompt(context.Background(), x)
		require.NoError(t, err)
		return outcome.Decision.Action
	}

	// Before an executor is set no model is known as a decision model, so
	// the auto engine asks by chat.
	assert.Equal(t, pluginapi.ActionAllow, run())
	assert.Equal(t, 1, chat)

	decider := &probabilityDecider{probability: "0.97"}
	service.SetDecider(decider)
	assert.Equal(t, pluginapi.ActionBlock, run())
	assert.Equal(t, 1, decider.calls)
	assert.Equal(t, 1, chat, "no further chat completion")

	service.SetDecider(&probabilityDecider{probability: "0.03"})
	assert.Equal(t, pluginapi.ActionAllow, run())
}
