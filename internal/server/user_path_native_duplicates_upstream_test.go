package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/requestparams"
)

// messagesHistories are the two ways a Messages request reaches Anthropic: a
// conversation only Anthropic reads is forwarded natively whatever it asks
// for, and any other one is forwarded natively while it is within the
// ceilings.
var messagesHistories = map[string]string{
	"a native-only request":  serverToolHistory,
	"a translatable request": hiMessages,
}

// spelled returns the raw values of object's members that spell key in any
// letter case, in document order, and whether each is named key itself.
func spelled(object, key string) (values []string, named bool) {
	named = true
	gjson.Parse(object).ForEach(func(name, value gjson.Result) bool {
		if strings.EqualFold(name.String(), key) {
			values = append(values, value.Raw)
			named = named && name.String() == key
		}
		return true
	})
	return values, named
}

// single returns the one value object holds under key, in any letter case,
// or "" when it holds none. More than one, or one under another spelling,
// fails the test: a decoder could read a different value than the gateway.
func single(t *testing.T, object, key string) string {
	t.Helper()
	values, named := spelled(object, key)
	require.LessOrEqual(t, len(values), 1, "%s is repeated upstream: %s", key, object)
	require.True(t, named, "%s is spelled in another letter case upstream: %s", key, object)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// A decoder may keep the first occurrence of a repeated member, or read only
// the exact name, where the gateway's reads the last in any letter case. A
// body that names its limit more than once therefore reaches Anthropic with
// one max_tokens, within the cap: the lowest the client named when the body
// is forwarded natively, and the one the gateway read, capped, when it takes
// the translated pipeline because that one is above the cap.
func TestNativeMessagesForwardOneLimitWithinTheCap(t *testing.T) {
	tests := []struct {
		name  string
		limit string
		// wantNative is the max_tokens a native-only request is forwarded
		// with, wantTranslatable that of a request the pipeline can carry.
		wantNative, wantTranslatable int
	}{
		{"review request: a re-cased duplicate after", `"max_tokens":100000,"MAX_TOKENS":5`, 5, 5},
		{"a re-cased duplicate before", `"MAX_TOKENS":5,"max_tokens":100000`, 5, 1000},
		{"review request: an exact duplicate", `"max_tokens":64000,"max_tokens":500`, 500, 500},
		{"an exact duplicate in the other order", `"max_tokens":500,"max_tokens":64000`, 500, 1000},
		{"duplicates that are all above the cap", `"max_tokens":64000,"Max_Tokens":32000,"max_tokens":16000`, 1000, 1000},
		{"only a re-cased member", `"Max_Tokens":64000`, 1000, 1000},
		{"a re-cased member within the cap on its own", `"MAX_TOKENS":500`, 500, 500},
		{"a null after the limit", `"max_tokens":64000,"max_tokens":null`, 1000, 1000},
		{"a null before the limit", `"MAX_TOKENS":null,"max_tokens":64000`, 1000, 1000},
	}
	for history, messages := range messagesHistories {
		for _, tt := range tests {
			t.Run(history+" with "+tt.name, func(t *testing.T) {
				f := newUpstreamFixture(t, cappedRows, nil)
				rec := f.post(t, "/tokens", "/v1/messages", `{"model":"anthropic/claude-sonnet-4-5",`+tt.limit+`,`+messages+`}`)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				want := tt.wantTranslatable
				if messages == serverToolHistory {
					want = tt.wantNative
				}
				assert.Equal(t, strconv.Itoa(want), single(t, string(f.upstreams["anthropic"].Last(t).Body), "max_tokens"))
			})
		}
	}
}

// A limit within the cap that the client spelled twice is not raised to the
// cap: the lowest one is what Anthropic gets.
func TestNativeMessagesKeepTheLowestRepeatedLimit(t *testing.T) {
	for name, limit := range map[string]string{
		"the lower one first": `"max_tokens":5,"MAX_TOKENS":100000`,
		"the lower one last":  `"max_tokens":100000,"MAX_TOKENS":5`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, "/tokens", "/v1/messages", `{"model":"anthropic/claude-sonnet-4-5",`+limit+`,`+serverToolHistory+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, `{"model":"claude-sonnet-4-5","max_tokens":5,`+serverToolHistory+`}`, string(f.upstreams["anthropic"].Last(t).Body))
		})
	}
}

// The same holds for the reasoning controls: thinking and output_config each
// reach Anthropic once, with one type, budget and effort, within the ceiling.
func TestNativeMessagesForwardOneOfEachReasoningControlWithinTheCeiling(t *testing.T) {
	const budget = `"type":"enabled","budget_tokens":60000`
	controls := map[string]string{
		"a repeated thinking, on first":         `"thinking":{` + budget + `},"thinking":{"type":"disabled"}`,
		"a repeated thinking, on last":          `"thinking":{"type":"disabled"},"thinking":{` + budget + `}`,
		"a re-cased thinking, on first":         `"THINKING":{` + budget + `},"thinking":{"type":"disabled"}`,
		"a re-cased thinking, on last":          `"thinking":{"type":"disabled"},"Thinking":{` + budget + `}`,
		"a repeated budget, the higher first":   `"thinking":{` + budget + `,"budget_tokens":2000}`,
		"a repeated budget, the higher last":    `"thinking":{"type":"enabled","budget_tokens":2000,"budget_tokens":60000}`,
		"a re-cased budget, the higher first":   `"thinking":{` + budget + `,"BUDGET_TOKENS":2000}`,
		"a re-cased budget, the higher last":    `"thinking":{"type":"enabled","Budget_Tokens":2000,"budget_tokens":60000}`,
		"a repeated type, on first":             `"thinking":{` + budget + `,"type":"disabled"}`,
		"a repeated type, on last":              `"thinking":{"type":"disabled","budget_tokens":60000,"type":"enabled"}`,
		"a re-cased type, on first":             `"thinking":{` + budget + `,"TYPE":"disabled"}`,
		"a re-cased type, on last":              `"thinking":{"Type":"disabled","budget_tokens":60000,"type":"enabled"}`,
		"a repeated effort, the higher first":   `"output_config":{"effort":"max","effort":"low"}`,
		"a repeated effort, the higher last":    `"output_config":{"effort":"low","effort":"max"}`,
		"a re-cased effort, the higher first":   `"output_config":{"effort":"max","EFFORT":"low"}`,
		"a re-cased effort, the higher last":    `"output_config":{"Effort":"low","effort":"max"}`,
		"a repeated output_config, high first":  `"output_config":{"effort":"max"},"output_config":{"effort":"low"}`,
		"a repeated output_config, high last":   `"output_config":{"effort":"low"},"output_config":{"effort":"max"}`,
		"a re-cased output_config, high first":  `"output_config":{"effort":"max"},"OUTPUT_CONFIG":{"effort":"low"}`,
		"a re-cased output_config, high last":   `"Output_Config":{"effort":"low"},"output_config":{"effort":"max"}`,
		"adaptive thinking and a hidden effort": `"thinking":{"type":"adaptive"},"output_config":{"effort":"max","EFFORT":"low"}`,
	}
	for history, messages := range messagesHistories {
		for name, control := range controls {
			t.Run(history+" with "+name, func(t *testing.T) {
				f := newUpstreamFixture(t, cappedRows, nil)
				model := "anthropic/claude-sonnet-4-5"
				if strings.Contains(control, "output_config") {
					model = "anthropic/claude-opus-5"
				}
				rec := f.post(t, "/low", "/v1/messages", `{"model":"`+model+`","max_tokens":64000,`+control+`,`+messages+`}`)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				assertReasoningWithin(t, string(f.upstreams["anthropic"].Last(t).Body), "low", 5000)
			})
		}
	}
}

// assertReasoningWithin checks that a Messages body carries each reasoning
// control once at most and asks for no more than the ceiling level, whose
// thinking budget is budget.
func assertReasoningWithin(t *testing.T, body, ceiling string, budget int) {
	t.Helper()
	thinking := single(t, body, "thinking")
	kind := gjson.Parse(single(t, thinking, "type")).String()
	tokens := single(t, thinking, "budget_tokens")
	effort := single(t, single(t, body, "output_config"), "effort")

	switch kind {
	case "enabled":
		sent, err := strconv.Atoi(tokens)
		require.NoError(t, err, body)
		assert.LessOrEqual(t, sent, budget, body)
	case "adaptive":
		assert.NotEmpty(t, effort, "adaptive thinking without an effort runs at high: %s", body)
	default:
		assert.Contains(t, []string{"", "disabled"}, kind, body)
	}
	if effort != "" {
		rank := requestparams.EffortRank(gjson.Parse(effort).String())
		assert.GreaterOrEqual(t, rank, 0, body)
		assert.LessOrEqual(t, rank, requestparams.EffortRank(ceiling), body)
	}
}
