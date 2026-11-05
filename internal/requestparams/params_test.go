package requestparams

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/validation"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestNormalize(t *testing.T) {
	long := strings.Repeat("k", MaxKeyLen+1)
	tests := []struct {
		name      string
		set       Set
		allowNull bool
		want      Set
		wantErr   string
	}{
		{name: "empty set is nil", set: Set{}, want: nil},
		{
			name: "values are compacted",
			set:  Set{"chat_template_kwargs": raw(`{ "enable_thinking" : true }`), "top_k": raw(" 20 ")},
			want: Set{"chat_template_kwargs": raw(`{"enable_thinking":true}`), "top_k": raw("20")},
		},
		{name: "null allowed in forced params", set: Set{"top_k": nil}, allowNull: true, want: Set{"top_k": nil}},
		{name: "null literal allowed in forced params", set: Set{"top_k": raw("null")}, allowNull: true, want: Set{"top_k": nil}},
		{name: "null refused in defaults", set: Set{"top_k": nil}, wantErr: `parameter "top_k" must have a value`},
		{name: "invalid JSON", set: Set{"top_k": raw("{")}, wantErr: "not valid JSON"},
		{name: "frozen key", set: Set{"model": raw(`"x"`)}, wantErr: `parameter "model" cannot be configured`},
		{name: "reasoning effort has its own field", set: Set{"reasoning_effort": raw(`"low"`)}, wantErr: `"reasoning_effort" cannot be configured`},
		{name: "padded key", set: Set{" top_k": raw("1")}, wantErr: "cannot be empty or padded"},
		{name: "empty key", set: Set{"": raw("1")}, wantErr: "cannot be empty or padded"},
		{name: "long key", set: Set{long: raw("1")}, wantErr: "longer than"},
		{name: "control characters", set: Set{"a\nb": raw("1")}, wantErr: "control characters"},
		{name: "whitespace in a key", set: Set{"top k": raw("1")}, wantErr: "contains whitespace"},
		{name: "frozen key in another case", set: Set{"Model": raw(`"x"`)}, wantErr: `parameter "Model" cannot be configured`},
		{name: "seed must be an integer", set: Set{"seed": raw(`"x"`)}, wantErr: `"seed" has the wrong type`},
		{name: "n must be an integer", set: Set{"n": raw(`"two"`)}, wantErr: `"n" has the wrong type`},
		{name: "stop must be text", set: Set{"stop": raw(`5`)}, wantErr: `"stop" has the wrong type`},
		{name: "stop array ok", set: Set{"stop": raw(`["a","b"]`), "seed": raw("7")}, want: Set{"stop": raw(`["a","b"]`), "seed": raw("7")}},
		{name: "stop string ok", set: Set{"stop": raw(`"a"`)}, want: Set{"stop": raw(`"a"`)}},
		{name: "stop elements must be strings", set: Set{"stop": raw(`["a",null]`)}, wantErr: "element 1 must be a string"},
		{name: "n must be at least 1", set: Set{"n": raw("0")}, wantErr: `"n" has the wrong type`},
		{name: "response_format needs a type", set: Set{"response_format": raw(`{"json_schema":{}}`)}, wantErr: `"response_format" has the wrong type`},
		{name: "json_schema must be an object", set: Set{"response_format": raw(`{"type":"json_schema","json_schema":"bad"}`)}, wantErr: "json_schema must be an object"},
		{name: "json_schema format ok", set: Set{"response_format": raw(`{"type":"json_schema","json_schema":{"name":"s"}}`)}, want: Set{"response_format": raw(`{"type":"json_schema","json_schema":{"name":"s"}}`)}},
		{name: "verbosity must be a string", set: Set{"verbosity": raw("1")}, wantErr: `"verbosity" has the wrong type`},
		{name: "text must be an object", set: Set{"text": raw(`"low"`)}, wantErr: `"text" has the wrong type`},
		{name: "response_format and text.format in one set", set: Set{"response_format": raw(`{"type":"json_object"}`), "text": raw(`{"format":{"type":"text"}}`)}, wantErr: `"response_format" and text.format set the same thing`},
		{name: "verbosity beside a text object without it", set: Set{"verbosity": raw(`"low"`), "text": raw(`{"format":{"type":"text"}}`)}, want: Set{"verbosity": raw(`"low"`), "text": raw(`{"format":{"type":"text"}}`)}},
		{name: "two spellings of the token limit", set: Set{"max_tokens": raw("1"), "max_output_tokens": raw("2")}, wantErr: `"max_output_tokens" and "max_tokens" set the same token limit`},
		{name: "temperature must be a number", set: Set{"temperature": raw(`"hot"`)}, wantErr: `"temperature" has the wrong type`},
		{name: "max_tokens must be an integer", set: Set{"max_tokens": raw(`1.5`)}, wantErr: `"max_tokens" has the wrong type`},
		{name: "max_tokens must not be negative", set: Set{"max_tokens": raw(`-1`)}, wantErr: `"max_tokens" has the wrong type`},
		{name: "a token limit must be at least 1", set: Set{"max_completion_tokens": raw(`0`)}, wantErr: `"max_completion_tokens" has the wrong type: must be at least 1`},
		{name: "parallel_tool_calls must be a bool", set: Set{"parallel_tool_calls": raw(`1`)}, wantErr: "wrong type"},
		{name: "user must be a string", set: Set{"user": raw(`1`)}, wantErr: "wrong type"},
		{name: "tool_choice string or object", set: Set{"tool_choice": raw(`[1]`)}, wantErr: "wrong type"},
		{name: "tool_choice object ok", set: Set{"tool_choice": raw(`{"type":"function"}`)}, want: Set{"tool_choice": raw(`{"type":"function"}`)}},
		{name: "metadata must be string map", set: Set{"metadata": raw(`{"a":1}`)}, wantErr: "wrong type"},
		{name: "metadata members cannot be null", set: Set{"metadata": raw(`{"a":null}`)}, wantErr: `member "a" must be a string`},
		{name: "include must be a string array", set: Set{"include": raw(`"reasoning"`)}, wantErr: `"include" has the wrong type`},
		{name: "include members must be strings", set: Set{"include": raw(`[null]`)}, wantErr: `"include" has the wrong type`},
		{name: "include array ok", set: Set{"include": raw(`["reasoning.encrypted_content"]`)}, want: Set{"include": raw(`["reasoning.encrypted_content"]`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize("params", tt.set, tt.allowNull)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.True(t, validation.IsError(err), "must be a validation error")
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeLimits(t *testing.T) {
	tooMany := make(Set, MaxKeys+1)
	for i := range MaxKeys + 1 {
		tooMany["k"+strings.Repeat("x", i%10)+string(rune('a'+i%26))+strings.Repeat("y", i/26)] = raw("1")
	}
	_, err := Normalize("params", tooMany, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most")

	big := Set{"blob": raw(`"` + strings.Repeat("x", MaxBytes) + `"`)}
	_, err = Normalize("params", big, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceed")

	nullKey := Set{strings.Repeat("k", MaxKeyLen): nil, "blob": raw(`"` + strings.Repeat("x", MaxBytes-MaxKeyLen-8) + `"`)}
	_, err = Normalize("params", nullKey, true)
	require.Error(t, err, "a stripped key still counts toward the size limit")
	assert.Contains(t, err.Error(), "exceed")
}

func TestDisjoint(t *testing.T) {
	require.NoError(t, Disjoint(Set{"a": raw("1")}, Set{"b": raw("2")}))
	err := Disjoint(Set{"a": raw("1")}, Set{"a": raw("2")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"a" cannot be in both`)
	err = Disjoint(Set{"max_tokens": raw("1")}, Set{"max_completion_tokens": raw("2")})
	require.Error(t, err, "token-limit spellings are one parameter")
	assert.Contains(t, err.Error(), `"max_tokens" cannot be in both`)
}

// response_format and verbosity set members of the Responses text object, so
// rows layer them with text member by member: the deeper row's setting wins
// whichever spelling either row used.
func TestLayerTreatsTextMembersAsOneSetting(t *testing.T) {
	inner := Overrides{Params: Set{"text": raw(`{"format":{"type":"text"}}`)}}
	outerFormat := Overrides{Params: Set{"response_format": raw(`{"type":"json_object"}`)}}
	assert.Equal(t, Set{"text": raw(`{"format":{"type":"text"}}`)}, outerFormat.Layer(inner).Params,
		"an inner text.format replaces an outer response_format")

	outerText := Overrides{Params: Set{"text": raw(`{"format":{"type":"text"},"verbosity":"low"}`)}}
	layered := outerText.Layer(Overrides{Params: Set{"response_format": raw(`{"type":"json_object"}`)}})
	assert.Equal(t, Set{"text": raw(`{"verbosity":"low"}`), "response_format": raw(`{"type":"json_object"}`)}, layered.Params,
		"an inner response_format removes only the outer text.format")

	err := Disjoint(Set{"response_format": raw(`{"type":"json_object"}`)}, Set{"text": raw(`{"format":{"type":"text"}}`)})
	require.Error(t, err, "a default and a forced value for the same text member")
}

// The token-limit spellings are one parameter when rows layer: the deeper
// row's spelling replaces the shallower one's, forced or default.
func TestLayerTreatsTokenLimitSpellingsAsOneKey(t *testing.T) {
	outer := Overrides{Params: Set{"max_tokens": raw("100")}}
	forced := outer.Layer(Overrides{Params: Set{"max_completion_tokens": raw("50")}})
	assert.Equal(t, Set{"max_completion_tokens": raw("50")}, forced.Params)

	unforced := outer.Layer(Overrides{Defaults: Set{"max_output_tokens": raw("50")}})
	assert.Empty(t, unforced.Params)
	assert.Equal(t, Set{"max_output_tokens": raw("50")}, unforced.Defaults)
}

func TestMergeAndLayer(t *testing.T) {
	base := Set{"a": raw("1"), "b": raw("2")}
	over := Set{"b": raw("3"), "c": raw("4")}
	merged := Merge(base, over)
	assert.Equal(t, Set{"a": raw("1"), "b": raw("3"), "c": raw("4")}, merged)
	assert.Equal(t, Set{"a": raw("1"), "b": raw("2")}, base, "inputs are not mutated")
	assert.Equal(t, base, Merge(base, nil))
	assert.Equal(t, over, Merge(nil, over))

	outer := Overrides{ReasoningEffort: "high", Params: Set{"top_k": raw("20")}, Defaults: Set{"temperature": raw("1")}}
	inner := Overrides{Params: Set{"top_k": raw("5")}, Defaults: Set{"top_p": raw("0.9")}}
	layered := outer.Layer(inner)
	assert.Equal(t, "high", layered.ReasoningEffort, "an inner row without an effort keeps the outer one")
	assert.Equal(t, Set{"top_k": raw("5")}, layered.Params)
	assert.Equal(t, Set{"temperature": raw("1"), "top_p": raw("0.9")}, layered.Defaults)
	assert.True(t, Overrides{}.IsZero())
	assert.False(t, layered.IsZero())
}

// A deeper row decides whether a key is forced or a default: its default
// unforces a shallower row's value and its forced value drops a shallower
// row's default.
func TestLayerPrecedenceAcrossSets(t *testing.T) {
	outer := Overrides{Params: Set{"temperature": raw("1"), "top_k": raw("20")}, Defaults: Set{"top_p": raw("0.9"), "min_p": raw("0")}}
	inner := Overrides{Defaults: Set{"temperature": raw("0.5")}, Params: Set{"top_p": raw("0.5")}}
	layered := outer.Layer(inner)
	assert.Equal(t, Set{"top_k": raw("20"), "top_p": raw("0.5")}, layered.Params)
	assert.Equal(t, Set{"min_p": raw("0"), "temperature": raw("0.5")}, layered.Defaults)
	assert.Equal(t, Set{"temperature": raw("1"), "top_k": raw("20")}, outer.Params, "inputs are not mutated")

	flipped := Overrides{Params: Set{"b": raw("1")}}.Layer(Overrides{Defaults: Set{"b": raw("2")}}).Layer(Overrides{Params: Set{"b": raw("3")}})
	assert.Equal(t, Overrides{Params: Set{"b": raw("3")}}, flipped, "the deepest row decides whether a key is forced")
	unforced := Overrides{Params: Set{"b": raw("1")}}.Layer(Overrides{Defaults: Set{"b": raw("2")}})
	assert.Equal(t, Overrides{Defaults: Set{"b": raw("2")}}, unforced)
}

func TestFromAny(t *testing.T) {
	set, err := FromAny(map[string]any{"temperature": 1.0, "top_k": nil, "chat_template_kwargs": map[string]any{"enable_thinking": true}})
	require.NoError(t, err)
	assert.Equal(t, raw("1"), set["temperature"])
	assert.Nil(t, set["top_k"])
	assert.JSONEq(t, `{"enable_thinking":true}`, string(set["chat_template_kwargs"]))

	empty, err := FromAny(nil)
	require.NoError(t, err)
	assert.Nil(t, empty)
}

func TestClone(t *testing.T) {
	set := Set{"a": raw("1"), "b": nil}
	cloned := Clone(set)
	cloned["a"][0] = '9'
	assert.Equal(t, raw("1"), set["a"])
	assert.Nil(t, cloned["b"])
	assert.Nil(t, Clone(nil))
}
