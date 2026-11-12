package requestparams

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestNormalizeReasoningEffort(t *testing.T) {
	effort, err := NormalizeReasoningEffort("max_reasoning_effort", " Medium ")
	require.NoError(t, err)
	assert.Equal(t, "medium", effort)

	effort, err = NormalizeReasoningEffort("max_reasoning_effort", "")
	require.NoError(t, err)
	assert.Empty(t, effort)

	_, err = NormalizeReasoningEffort("max_reasoning_effort", "extreme")
	require.ErrorContains(t, err, `unknown max_reasoning_effort "extreme"`)
}

func TestEffortRank(t *testing.T) {
	assert.Less(t, EffortRank("low"), EffortRank("medium"))
	assert.Less(t, EffortRank("high"), EffortRank(" XHigh "))
	assert.Equal(t, 0, EffortRank("none"))
	assert.Equal(t, -1, EffortRank("extreme"))
	assert.Equal(t, -1, EffortRank(""))
}

func TestCeilingsTightenKeepsTheLowest(t *testing.T) {
	tests := []struct {
		name        string
		base, other Ceilings
		want        Ceilings
	}{
		{"nothing set", Ceilings{}, Ceilings{}, Ceilings{}},
		{"other fills an unset ceiling", Ceilings{}, Ceilings{ReasoningEffort: "high", OutputTokens: 4096}, Ceilings{ReasoningEffort: "high", OutputTokens: 4096}},
		{"unset other changes nothing", Ceilings{ReasoningEffort: "low", OutputTokens: 100}, Ceilings{}, Ceilings{ReasoningEffort: "low", OutputTokens: 100}},
		{"lower other wins", Ceilings{ReasoningEffort: "high", OutputTokens: 4096}, Ceilings{ReasoningEffort: "low", OutputTokens: 1024}, Ceilings{ReasoningEffort: "low", OutputTokens: 1024}},
		{"higher other cannot loosen", Ceilings{ReasoningEffort: "low", OutputTokens: 1024}, Ceilings{ReasoningEffort: "max", OutputTokens: 100000}, Ceilings{ReasoningEffort: "low", OutputTokens: 1024}},
		{"each ceiling is tightened on its own", Ceilings{ReasoningEffort: "low", OutputTokens: 4096}, Ceilings{ReasoningEffort: "high", OutputTokens: 1024}, Ceilings{ReasoningEffort: "low", OutputTokens: 1024}},
		{"none is the lowest effort", Ceilings{ReasoningEffort: "minimal"}, Ceilings{ReasoningEffort: "none"}, Ceilings{ReasoningEffort: "none"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.base.Tighten(tt.other))
			assert.Equal(t, tt.want, tt.other.Tighten(tt.base), "tightening is symmetric")
		})
	}
}

func TestOverridesLayerTightensCeilingsBothWays(t *testing.T) {
	route := Overrides{ReasoningEffort: "high", Ceilings: Ceilings{ReasoningEffort: "medium"}}
	deeper := Overrides{Ceilings: Ceilings{ReasoningEffort: "xhigh", OutputTokens: 512}}

	layered := route.Layer(deeper)
	assert.Equal(t, "high", layered.ReasoningEffort, "a forced effort still layers as before")
	assert.Equal(t, Ceilings{ReasoningEffort: "medium", OutputTokens: 512}, layered.Ceilings)

	assert.True(t, Overrides{}.IsZero())
	assert.False(t, Overrides{Ceilings: Ceilings{OutputTokens: 1}}.IsZero())
	assert.False(t, Overrides{Ceilings: Ceilings{OutputTokens: 1}}.Forces(), "ceilings alone force nothing")
	assert.True(t, Overrides{ReasoningEffort: "low"}.Forces())
}

func TestCapChatTokens(t *testing.T) {
	tests := []struct {
		name       string
		maxTokens  *int
		extras     map[string]json.RawMessage
		limit      int
		wantTokens *int
		wantExtras map[string]string
		unchanged  bool
	}{
		{name: "no limit set by the client stays unset", limit: 100, unchanged: true},
		{name: "lower client limit is kept", maxTokens: new(50), limit: 100, wantTokens: new(50), unchanged: true},
		{name: "equal client limit is kept", maxTokens: new(100), limit: 100, wantTokens: new(100), unchanged: true},
		{name: "higher client limit is lowered", maxTokens: new(4000), limit: 100, wantTokens: new(100)},
		{name: "no ceiling changes nothing", maxTokens: new(4000), limit: 0, wantTokens: new(4000), unchanged: true},
		{
			name:       "max_completion_tokens is lowered in the client's spelling",
			extras:     map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(`9000`), "seed": json.RawMessage(`7`)},
			limit:      100,
			wantExtras: map[string]string{"max_completion_tokens": `100`, "seed": `7`},
		},
		{
			name:       "max_completion_tokens within the ceiling is kept",
			extras:     map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(`64`)},
			limit:      100,
			wantExtras: map[string]string{"max_completion_tokens": `64`},
			unchanged:  true,
		},
		{
			name:       "every spelling above the ceiling is lowered",
			maxTokens:  new(500),
			extras:     map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(`9000`), "max_output_tokens": json.RawMessage(`20`)},
			limit:      100,
			wantTokens: new(100),
			wantExtras: map[string]string{"max_completion_tokens": `100`, "max_output_tokens": `20`},
		},
		{
			name:       "a limit that is not a number cannot pass the ceiling",
			extras:     map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(`"lots"`)},
			limit:      100,
			wantExtras: map[string]string{"max_completion_tokens": `100`},
		},
		{
			name:       "a null limit counts as unset",
			extras:     map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(`null`)},
			limit:      100,
			wantExtras: map[string]string{"max_completion_tokens": `null`},
			unchanged:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{Model: "m", MaxTokens: tt.maxTokens, ExtraFields: core.UnknownJSONFieldsFromMap(tt.extras)}
			client := clonePtr(tt.maxTokens)

			capped, err := CapChatTokens(req, tt.limit)
			require.NoError(t, err)

			if tt.unchanged {
				assert.Same(t, req, capped)
			} else {
				assert.NotSame(t, req, capped)
			}
			assert.Equal(t, tt.wantTokens, capped.MaxTokens)
			for key, want := range tt.wantExtras {
				assert.JSONEq(t, want, string(capped.ExtraFields.Lookup(key)), key)
			}
			assert.Equal(t, client, req.MaxTokens, "the client's request must not be mutated")
		})
	}
}

func TestCapResponsesTokens(t *testing.T) {
	tests := []struct {
		name       string
		maxTokens  *int
		extras     map[string]json.RawMessage
		limit      int
		wantTokens *int
		wantExtras map[string]string
		unchanged  bool
	}{
		{name: "no limit set by the client stays unset", limit: 100, unchanged: true},
		{name: "lower client limit is kept", maxTokens: new(50), limit: 100, wantTokens: new(50), unchanged: true},
		{name: "higher client limit is lowered", maxTokens: new(4000), limit: 100, wantTokens: new(100)},
		{
			name:       "chat spellings are lowered too",
			extras:     map[string]json.RawMessage{"max_tokens": json.RawMessage(`9000`), "max_completion_tokens": json.RawMessage(`10`)},
			limit:      100,
			wantExtras: map[string]string{"max_tokens": `100`, "max_completion_tokens": `10`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{Model: "m", MaxOutputTokens: tt.maxTokens, ExtraFields: core.UnknownJSONFieldsFromMap(tt.extras)}
			client := clonePtr(tt.maxTokens)

			capped, err := CapResponsesTokens(req, tt.limit)
			require.NoError(t, err)

			if tt.unchanged {
				assert.Same(t, req, capped)
			} else {
				assert.NotSame(t, req, capped)
			}
			assert.Equal(t, tt.wantTokens, capped.MaxOutputTokens)
			for key, want := range tt.wantExtras {
				assert.JSONEq(t, want, string(capped.ExtraFields.Lookup(key)), key)
			}
			assert.Equal(t, client, req.MaxOutputTokens, "the client's request must not be mutated")
		})
	}
}

func clonePtr(value *int) *int {
	if value == nil {
		return nil
	}
	return new(*value)
}
