package server

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstreamLimits reads the output token limit each fake upstream received,
// from the field its API takes it in.
var upstreamLimits = map[string]func(t *testing.T, sent map[string]any) int{
	"anthropic": func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_tokens") },
	"cohere":    func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_tokens") },
	"gemini": func(t *testing.T, sent map[string]any) int {
		return number(t, object(t, sent, "generationConfig"), "maxOutputTokens")
	},
	"openai": func(t *testing.T, sent map[string]any) int {
		// An OpenAI-compatible upstream reads whichever spelling it knows, in
		// any letter case when it matches names the way Go does, so every one
		// it receives is a whole number within the cap.
		for key := range sent {
			if slices.Contains([]string{"max_tokens", "max_completion_tokens", "max_output_tokens"}, strings.ToLower(key)) {
				assert.InDelta(t, 500.5, number(t, sent, key), 499.5, key)
			}
		}
		return openAILimit(t, sent)
	},
}

// A capped caller's limit reaches the upstream in the field the serving
// adapter reads, whatever spelling the request used, and a value no adapter
// takes as a limit (below 1, fractional, null) is replaced by the cap.
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

					assert.Equal(t, limit.want, upstreamLimits[target.upstream](t, f.sent(t, target.upstream)))
				})
			}
		}
	}
}
