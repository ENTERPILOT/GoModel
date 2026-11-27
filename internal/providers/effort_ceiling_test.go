package providers

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// threeLevels is an adapter whose model takes low, high and max, and rounds
// the gateway's other levels up to them.
func threeLevels(req *core.ChatRequest) string {
	switch effort := SentEffort(req); effort {
	case "none", "minimal":
		return "low"
	case "medium":
		return "high"
	case "xhigh":
		return "max"
	default:
		return effort
	}
}

func TestChatWithinEffortCeiling(t *testing.T) {
	nested := func(effort string) *core.ChatRequest {
		return &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Effort: effort, Summary: "auto"}}
	}
	flat := func(effort string) *core.ChatRequest {
		var req core.ChatRequest
		require.NoError(t, json.Unmarshal([]byte(`{"model":"m","reasoning_effort":"`+effort+`","top_k":20}`), &req))
		return &req
	}
	tests := []struct {
		name    string
		req     *core.ChatRequest
		ceiling string
		sent    func(*core.ChatRequest) string
		// want is the effort of the result; "" means req is returned itself.
		want string
	}{
		{"no ceiling", nested("xhigh"), "", threeLevels, ""},
		{"the level sent is within the ceiling", nested("low"), "medium", threeLevels, ""},
		{"a level rounded above the ceiling gets the highest one within it", nested("medium"), "medium", threeLevels, "low"},
		{"the ceiling itself when the model takes it", nested("xhigh"), "high", threeLevels, "high"},
		{"one level down is enough", nested("xhigh"), "xhigh", threeLevels, "high"},
		{"a flat effort is lowered where it stands", flat("medium"), "medium", threeLevels, "low"},
		{"a model that takes no level within the ceiling keeps its lowest", nested("none"), "none", threeLevels, ""},
		{"a level the scale cannot place is left alone", nested("default"), "low", threeLevels, ""},
		{"an adapter that sends no effort", nested("high"), "low", func(*core.ChatRequest) string { return "" }, ""},
		{"an effort the adapter drops is no level within the ceiling", nested("medium"), "low", func(req *core.ChatRequest) string {
			if SentEffort(req) == "medium" {
				return "high"
			}
			return ""
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := core.WithRequestCeilings(context.Background(), core.RequestCeilings{ReasoningEffort: tt.ceiling})
			original := SentEffort(tt.req)

			got := ChatWithinEffortCeiling(ctx, tt.req, tt.sent)

			assert.Equal(t, original, SentEffort(tt.req), "the request must not be mutated")
			if tt.want == "" {
				assert.Same(t, tt.req, got)
				return
			}
			require.NotSame(t, tt.req, got)
			assert.Equal(t, tt.want, SentEffort(got))
			if tt.req.Reasoning != nil {
				assert.Equal(t, "auto", got.Reasoning.Summary, "the rest of the reasoning object stays")
				return
			}
			assert.Nil(t, got.Reasoning, "a flat effort stays flat")
			assert.Len(t, got.ExtraFields.LookupAll("reasoning_effort"), 1)
			assert.JSONEq(t, `20`, string(got.ExtraFields.Lookup("top_k")))
		})
	}

	assert.Nil(t, ChatWithinEffortCeiling(context.Background(), nil, threeLevels))
}

func TestSentEffort(t *testing.T) {
	var flat core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"m","reasoning_effort":" high "}`), &flat))
	var notAString core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"m","reasoning_effort":3}`), &notAString))

	assert.Empty(t, SentEffort(nil))
	assert.Empty(t, SentEffort(&core.ChatRequest{}))
	assert.Equal(t, "low", SentEffort(&core.ChatRequest{Reasoning: &core.Reasoning{Effort: " low "}}))
	assert.Equal(t, "high", SentEffort(&flat))
	assert.Empty(t, SentEffort(&notAString))

	nestedWins := flat
	nestedWins.Reasoning = &core.Reasoning{Effort: "low"}
	assert.Equal(t, "low", SentEffort(&nestedWins), "adapters read the nested effort first")
	summaryOnly := flat
	summaryOnly.Reasoning = &core.Reasoning{Summary: "auto"}
	assert.Equal(t, "high", SentEffort(&summaryOnly))
}
