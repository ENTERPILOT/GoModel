package providers

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// A client cannot know each provider's stop-sequence limit, so an over-long
// list is truncated rather than rejected upstream. Everything else about the
// request, including other extra fields, must survive untouched.
func TestCapStopSequences(t *testing.T) {
	cases := []struct {
		name  string
		stop  string
		limit int
		want  string
	}{
		{name: "within the limit", stop: `["a","b"]`, limit: 4, want: `["a","b"]`},
		{name: "at the limit", stop: `["a","b","c","d"]`, limit: 4, want: `["a","b","c","d"]`},
		{name: "over the limit", stop: `["a","b","c","d","e","f"]`, limit: 4, want: `["a","b","c","d"]`},
		{name: "bare string", stop: `"a"`, limit: 4, want: `"a"`},
		{name: "no limit", stop: `["a","b","c","d","e"]`, limit: 0, want: `["a","b","c","d","e"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
				"stop": json.RawMessage(tc.stop),
				"seed": json.RawMessage("7"),
			})
			require.NoError(t, err)
			req := &core.ChatRequest{Model: "gpt-4.1-mini", ExtraFields: extra}

			adapted, err := CapStopSequences(req, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(adapted.ExtraFields.Lookup("stop")))
			assert.Equal(t, "7", string(adapted.ExtraFields.Lookup("seed")))
			assert.Equal(t, tc.stop, string(req.ExtraFields.Lookup("stop")), "original request mutated")
		})
	}
}

func TestCapStopSequencesWithoutStopField(t *testing.T) {
	req := &core.ChatRequest{Model: "gpt-4.1-mini"}
	adapted, err := CapStopSequences(req, 4)
	require.NoError(t, err)
	assert.Same(t, req, adapted, "request without a stop field should be returned unchanged")

	adapted, err = CapStopSequences(nil, 4)
	require.NoError(t, err)
	assert.Nil(t, adapted)
}
