package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// usageDecodeInputs are usage objects for which UnmarshalJSON's typed decode
// must agree with the member-by-member decode it replaced.
var usageDecodeInputs = []string{
	// OpenAI.
	`{"prompt_tokens":17,"completion_tokens":4,"total_tokens":21,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}`,
	// xAI.
	`{"prompt_tokens":10,"completion_tokens":35,"total_tokens":420,"prompt_tokens_details":{"text_tokens":10,"audio_tokens":0,"image_tokens":0,"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":375,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0},"num_sources_used":0,"cost_in_usd_ticks":2073250}`,
	// Groq.
	`{"queue_time":0.089910694,"prompt_tokens":38,"prompt_time":0.001689387,"completion_tokens":4,"completion_time":0.030590271,"total_tokens":42,"total_time":0.032279658}`,
	// Gemini OpenAI-compatible.
	`{"completion_tokens":2,"prompt_tokens":4,"total_tokens":6}`,
	// OpenRouter.
	`{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"cost":0.0000135,"is_byok":false,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"cost_details":{"upstream_inference_cost":null},"completion_tokens_details":{"reasoning_tokens":0,"image_tokens":0}}`,
	// Anthropic and Gemini usage as the gateway re-encodes it.
	`{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":40},"cache_creation_input_tokens":10,"cache_read_input_tokens":40}`,
	`{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13,"completion_tokens_details":{"reasoning_tokens":3},"thoughts_token_count":3}`,
	`{"prompt_tokens":1,"raw_usage":{"cache_creation_input_tokens":5,"nested":{"a":[1,2]}}}`,
	`{"raw_usage":null}`,
	`{"raw_usage":{}}`,
	`{}`,
	`null`,
	` { "prompt_tokens" : 3 } `,
	`{"prompt_tokens":null,"prompt_tokens_details":null,"completion_tokens_details":null}`,
	`{"prompt_tokens_details":{"cached_tokens":1,"unknown_detail":7}}`,
	`{"prompt_tokens":-0,"total_tokens":9223372036854775807}`,
	`{"x":null,"y":[],"z":{},"":"empty key"}`,
	`{"a":1,"a":2}`,
	// Irregular objects take the member-by-member path.
	`{"prompt_tokens":1,"prompt_tokens":2}`,
	`{"prompt_tokens_details":{"cached_tokens":1},"prompt_tokens_details":{"audio_tokens":2}}`,
	`{"raw_usage":{"a":1},"raw_usage":{"b":2}}`,
	`{"PROMPT_TOKENS":5}`,
	`{"Raw_Usage":{"a":1}}`,
	`{"prompt_\u0074okens":5}`,
	`{"prompt_to\u212aens":5}`,
	`{"prompt_toKens":5}`,
	`{"żółć":1,"prompt_tokens":2}`,
	// Rejected.
	`{"prompt_tokens":5.0}`,
	`{"prompt_tokens":5e0}`,
	`{"prompt_tokens":"5"}`,
	`{"prompt_tokens":true}`,
	`{"prompt_tokens":92233720368547758070}`,
	`{"prompt_tokens_details":[]}`,
	`{"completion_tokens_details":{"reasoning_tokens":"1"}}`,
	`{"raw_usage":5}`,
	`{"raw_usage":[]}`,
	`[]`,
	`5`,
	`"usage"`,
	`{"prompt_tokens":1,}`,
	`{"x":tru}`,
	`{"x":[1,}`,
	`{"prompt_tokens":1`,
	``,
}

// decodeUsageBothWays decodes data with UnmarshalJSON and with the
// member-by-member decode, each starting from a copy of start.
func decodeUsageBothWays(data []byte, start Usage) (got, want Usage, gotErr, wantErr error) {
	got, want = start, start
	if start.PromptTokensDetails != nil {
		gotDetails, wantDetails := *start.PromptTokensDetails, *start.PromptTokensDetails
		got.PromptTokensDetails, want.PromptTokensDetails = &gotDetails, &wantDetails
	}
	gotErr = got.UnmarshalJSON(data)
	wantErr = want.unmarshalJSONMemberByMember(data)
	return got, want, gotErr, wantErr
}

// rawUsageCollides reports whether a member of the raw_usage object shares
// its name with a top-level member. The member-by-member decode merges them
// in map order, so its result is not deterministic for such objects.
func rawUsageCollides(data []byte) bool {
	root := gjson.ParseBytes(data)
	collides := false
	root.Get("raw_usage").ForEach(func(key, _ gjson.Result) bool {
		collides = root.Get(gjson.Escape(key.String())).Exists()
		return !collides
	})
	return collides
}

func assertUsageDecodesAgree(t *testing.T, data []byte) {
	t.Helper()
	if rawUsageCollides(data) {
		t.Skip("member-by-member decode is not deterministic here")
	}
	starts := []Usage{
		{},
		{PromptTokens: 9, TotalTokens: 9, PromptTokensDetails: &PromptTokensDetails{AudioTokens: 4}, RawUsage: map[string]any{"stale": true}},
	}
	for _, start := range starts {
		got, want, gotErr, wantErr := decodeUsageBothWays(data, start)
		if wantErr != nil {
			require.Error(t, gotErr, "input %q", data)
			continue
		}
		require.NoError(t, gotErr, "input %q", data)
		assert.Equal(t, want, got, "input %q", data)
	}
}

func TestUsageUnmarshalJSON_MatchesMemberByMemberDecode(t *testing.T) {
	for _, input := range usageDecodeInputs {
		t.Run(input, func(t *testing.T) {
			assertUsageDecodesAgree(t, []byte(input))
		})
	}
}

func TestUsageUnmarshalJSON_DecodesThroughParent(t *testing.T) {
	var resp ChatResponse
	require.NoError(t, json.Unmarshal([]byte(`{"id":"x","usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4,"cost":0.5}}`), &resp))
	assert.Equal(t, Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4, RawUsage: map[string]any{"cost": 0.5}}, resp.Usage)
}

func FuzzUsageUnmarshalJSON(f *testing.F) {
	for _, input := range usageDecodeInputs {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		assertUsageDecodesAgree(t, data)
	})
}
