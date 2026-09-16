package providers

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestAdaptReasoningEffortRequest(t *testing.T) {
	req := &core.ChatRequest{
		Model:     "some-model",
		Reasoning: &core.Reasoning{Effort: "high"},
	}

	adapted, err := AdaptReasoningEffortRequest(req, "high")
	require.NoError(t, err)
	require.Nil(t, adapted.Reasoning)
	require.NotNil(t, req.Reasoning)

	body, err := json.Marshal(adapted)
	require.NoError(t, err)

	var wire map[string]json.RawMessage
	err = json.Unmarshal(body, &wire)
	require.NoError(t, err)
	got := string(wire["reasoning_effort"])
	require.Equal(t, `"high"`, got)
	_, present := wire["reasoning"]
	require.False(t, present)
}

func TestAdaptReasoningEffortRequestPreservesExistingExtraFields(t *testing.T) {
	req := &core.ChatRequest{
		Model:     "some-model",
		Reasoning: &core.Reasoning{Effort: "low"},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"custom_field":     json.RawMessage(`"kept"`),
			"reasoning_effort": json.RawMessage(`"stale"`),
		}),
	}

	adapted, err := AdaptReasoningEffortRequest(req, "low")
	require.NoError(t, err)

	body, err := json.Marshal(adapted)
	require.NoError(t, err)

	var wire map[string]json.RawMessage
	err = json.Unmarshal(body, &wire)
	require.NoError(t, err)
	got := string(wire["custom_field"])
	require.Equal(t, `"kept"`, got)
	got = string(wire["reasoning_effort"])
	require.Equal(t, `"low"`, got)
}

func TestDropReasoning(t *testing.T) {
	req := &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Effort: "low"}}
	got := DropReasoning(req)
	require.Nil(t, got.Reasoning)
	require.NotNil(t, req.Reasoning)
	require.Equal(t, "m", got.Model, "DropReasoning mutated its input or lost fields: in=%+v out=%+v", req, got)
}
