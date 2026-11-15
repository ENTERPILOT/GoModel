package server

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenLimitMembers are the spellings of the output token limit an
// OpenAI-compatible upstream knows.
var tokenLimitMembers = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}

// upstreamLimits reads the output token limit each fake upstream received,
// from the field its API takes it in. path and model are the dialect and
// the model the request was sent with.
var upstreamLimits = map[string]func(t *testing.T, sent map[string]any, path, model string) int{
	"anthropic": func(t *testing.T, sent map[string]any, _, _ string) int { return number(t, sent, "max_tokens") },
	"cohere":    func(t *testing.T, sent map[string]any, _, _ string) int { return number(t, sent, "max_tokens") },
	"gemini": func(t *testing.T, sent map[string]any, _, _ string) int {
		return number(t, object(t, sent, "generationConfig"), "maxOutputTokens")
	},
	"openai": func(t *testing.T, sent map[string]any, path, model string) int {
		// An OpenAI-compatible upstream reads whichever spelling it knows, in
		// any letter case when it matches names the way Go does, so every one
		// it receives is a whole number within the cap.
		for key := range sent {
			if slices.Contains(tokenLimitMembers, strings.ToLower(key)) {
				assert.LessOrEqual(t, number(t, sent, key), 1000, key)
				assert.Positive(t, number(t, sent, key), key)
			}
		}
		// The limit is the highest one in a member the dialect and model take.
		fields, limit := openAILimitFields(path, model), 0
		for _, field := range fields {
			if _, ok := sent[field]; ok {
				limit = max(limit, number(t, sent, field))
			}
		}
		require.NotZero(t, limit, "none of %v in upstream body %v", fields, sent)
		return limit
	},
}

// openAILimitFields names the members OpenAI takes the limit in:
// max_output_tokens on the Responses API, max_completion_tokens on Chat
// Completions, and there max_tokens too unless the model reasons.
func openAILimitFields(path, model string) []string {
	switch {
	case path == "/v1/responses":
		return []string{"max_output_tokens"}
	case model == "openai/gpt-5":
		return []string{"max_completion_tokens"}
	default:
		return []string{"max_tokens", "max_completion_tokens"}
	}
}

// A capped caller's limit reaches the upstream in the field the serving
// adapter reads, whatever spelling the request used, and a value no adapter
// takes as a limit (below 1, fractional, null) is replaced by the cap. A
// limit within the cap is never raised, in whatever letter case it came.
func TestTokenCeilingReachesEveryUpstreamInEverySpelling(t *testing.T) {
	targets := []struct{ model, upstream string }{
		{"gemini/gemini-2.5-flash", "gemini"},
		{"cohere/command-a-reasoning", "cohere"},
		{"anthropic/claude-sonnet-4-5", "anthropic"},
		{"openai/gpt-4o", "openai"},
		{"openai/gpt-5", "openai"},
	}
	type sentLimit struct {
		fields string
		want   int
	}
	dialects := []struct {
		path, prompt string
		limits       []sentLimit
	}{
		{
			path: "/v1/chat/completions", prompt: hiMessages,
			limits: []sentLimit{
				{`"max_output_tokens":1000`, 1000},
				{`"max_output_tokens":500`, 500},
				{`"max_output_tokens":64000`, 1000},
				{`"max_tokens":null,"max_output_tokens":700`, 700},
				{`"max_tokens":-1`, 1000},
				{`"max_tokens":0`, 1000},
				{`"max_completion_tokens":500`, 500},
				{`"max_completion_tokens":0`, 1000},
				{`"max_completion_tokens":-1`, 1000},
				{`"max_completion_tokens":500.5`, 1000},
				{`"max_completion_tokens":"500"`, 1000},
				{`"max_tokens":500,"max_completion_tokens":null`, 500},
				{`"max_completion_tokens":null,"max_completion_tokens":64000`, 1000},
				{`"MAX_TOKENS":64000`, 1000},
				{`"max_tokens":500,"Max_Completion_Tokens":64000`, 500},
				{`"MAX_COMPLETION_TOKENS":20`, 20},
				{`"MAX_TOKENS":20`, 20},
				{`"Max_Completion_Tokens":20,"max_tokens":500`, 20},
				{`"Max_Output_Tokens":20,"MAX_TOKENS":0`, 20},
			},
		},
		{
			path: "/v1/responses", prompt: `"input":"hi"`,
			limits: []sentLimit{
				{`"max_tokens":500`, 500},
				{`"max_tokens":64000`, 1000},
				{`"max_completion_tokens":500`, 500},
				{`"max_completion_tokens":-1`, 1000},
				{`"max_output_tokens":-1`, 1000},
				{`"max_output_tokens":0`, 1000},
				{`"max_output_tokens":500,"max_tokens":64000`, 500},
				{`"max_output_tokens":500,"MAX_OUTPUT_TOKENS":64000`, 1000},
				{`"MAX_TOKENS":20`, 20},
				{`"Max_Output_Tokens":20`, 20},
				{`"max_output_tokens":500,"Max_Completion_Tokens":20`, 20},
			},
		},
	}
	for _, dialect := range dialects {
		for _, target := range targets {
			for _, limit := range dialect.limits {
				t.Run(dialect.path+" to "+target.model+" with "+limit.fields, func(t *testing.T) {
					f := newUpstreamFixture(t, cappedRows, nil)
					rec := f.post(t, "/tokens", dialect.path, `{"model":"`+target.model+`",`+limit.fields+`,`+dialect.prompt+`}`)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

					assert.Equal(t, limit.want, upstreamLimits[target.upstream](t, f.sent(t, target.upstream), dialect.path, target.model))
				})
			}
		}
	}
}
