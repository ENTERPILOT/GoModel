package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithSingleLimits(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // "" when the body is returned as it is
	}{
		{name: "each limit once", body: `{ "max_tokens": 5, "thinking": {"type": "enabled", "budget_tokens": 2000}, "output_config": {"effort": "low"} }`},
		{name: "no limit at all", body: `{"messages":[{"max_tokens":1,"MAX_TOKENS":2}]}`},
		{name: "members inside other members are not limits", body: `{"max_tokens":5,"metadata":{"max_tokens":1,"MAX_TOKENS":2,"thinking":1,"Thinking":2}}`},
		{
			name: "the lowest limit stands where the last one stood",
			body: `{"a":1,"max_tokens":9,"b":2,"MAX_TOKENS":5,"c":3,"max_tokens":7,"d":4}`,
			want: `{"a":1,"b":2,"c":3,"max_tokens":5,"d":4}`,
		},
		{name: "a re-cased limit gets the name Anthropic reads", body: `{"Max_Tokens":5,"a":1}`, want: `{"max_tokens":5,"a":1}`},
		{name: "a value that is no limit does not count as the lowest", body: `{"max_tokens":null,"max_tokens":0,"max_tokens":8,"max_tokens":-1}`, want: `{"max_tokens":8}`},
		{name: "values that are no limit leave the last", body: `{"max_tokens":0,"max_tokens":null}`, want: `{"max_tokens":null}`},
		{
			name: "the last thinking, with its last type and budget",
			body: `{"thinking":{"type":"enabled","budget_tokens":9},"THINKING":{"Type":"enabled","display":"summarized","budget_tokens":7,"type":"disabled","Budget_Tokens":3}}`,
			want: `{"thinking":{"display":"summarized","type":"disabled","budget_tokens":3}}`,
		},
		{
			name: "the last output_config, with its last effort and its format",
			body: `{"output_config":{"effort":"max"},"output_config":{"effort":"max","format":{"effort":"x","EFFORT":"y"},"EFFORT":"low"}}`,
			want: `{"output_config":{"format":{"effort":"x","EFFORT":"y"},"effort":"low"}}`,
		},
		{name: "a null control after an object", body: `{"thinking":{"type":"enabled"},"thinking":null}`, want: `{"thinking":null}`},
		{name: "an escaped name is the name", body: `{"\u006dax_tokens":5}`},
		{name: "an escaped name folds like the name", body: `{"\u006dax_tokens":9,"MAX_TOKENS":5}`, want: `{"max_tokens":5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, folded := withSingleLimits([]byte(tt.body))

			if tt.want == "" {
				assert.False(t, folded)
				assert.Equal(t, tt.body, string(got))
				return
			}
			assert.True(t, folded)
			assert.Equal(t, tt.want, string(got))
			again, folded := withSingleLimits(got)
			assert.False(t, folded, "a folded body spells each limit once")
			assert.Equal(t, tt.want, string(again))
		})
	}
}
