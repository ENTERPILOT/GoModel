package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reasoningMembers are the member names that set reasoning on an
// OpenAI-compatible upstream, lower-cased.
var reasoningMembers = []string{"reasoning", "reasoning_effort", "thinking", "enable_thinking", "thinking_budget", "think", "chat_template_kwargs", "extra_body"}

// An upstream that matches member names the way Go does reads a re-cased
// reasoning control as the control itself, and decoders differ in which
// occurrence of a repeated member they keep. Under an effort ceiling none of
// them reaches an OpenAI-compatible upstream: the only reasoning member left
// is the effort the gateway wrote.
func TestEffortCeilingReplacesRecasedAndRepeatedControls(t *testing.T) {
	dialects := []struct{ path, model, prompt, effort string }{
		{"/v1/chat/completions", "openai/gpt-5.1", hiMessages, "reasoning_effort"},
		{"/v1/responses", "openai/gpt-5", `"input":"hi"`, "reasoning"},
	}
	controls := map[string]string{
		"review request: a re-cased reasoning object": `"Reasoning":{"effort":"max"}`,
		"a re-cased object next to the typed one":     `"Reasoning":{"effort":"max"},"reasoning":{"effort":"low"}`,
		"a re-cased effort inside a re-cased object":  `"Reasoning":{"Effort":"max","effort":"low"},"reasoning":{"effort":"low"}`,
		"a re-cased flat effort":                      `"REASONING_EFFORT":"max"`,
		"re-cased provider controls":                  `"Thinking":{"type":"enabled"},"Enable_Thinking":true,"THINKING_BUDGET":32000,"Think":"high"`,
		"a re-cased type after a disabled one":        `"thinking":{"type":"disabled","budget_tokens":32000,"Type":"enabled"}`,
		"re-cased template switches":                  `"Chat_Template_Kwargs":{"enable_thinking":true},"chat_template_kwargs":{"Enable_Thinking":true}`,
		"a repeated template switch":                  `"chat_template_kwargs":{"enable_thinking":true,"enable_thinking":"on"}`,
		"re-cased extra_body members":                 `"Extra_Body":{"thinking_budget":32000},"extra_body":{"Thinking_Budget":32000}`,
		"a repeated extra_body member":                `"extra_body":{"thinking":{"type":"enabled","budget_tokens":32000},"thinking":{"type":"enabled"}}`,
		"a repeated extra_body":                       `"extra_body":{"think":"max"},"extra_body":null`,
	}
	for _, dialect := range dialects {
		for name, control := range controls {
			t.Run(dialect.path+" with "+name, func(t *testing.T) {
				f := newUpstreamFixture(t, cappedRows, nil)
				rec := f.post(t, "/low", dialect.path, `{"model":"`+dialect.model+`",`+dialect.prompt+`,`+control+`}`)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				sent := f.sent(t, "openai")
				assert.Equal(t, "low", openAIEffort(sent))
				for key, value := range sent {
					if key != dialect.effort && value != nil {
						assert.NotContains(t, reasoningMembers, strings.ToLower(key), "%s reached the upstream as %v", key, value)
					}
				}
			})
		}
	}
}

// A control that is off in the type a decoder keeps, the last one, asks for
// no reasoning, and so does a request that turns reasoning off next to a
// control the scale cannot rank: a budget means nothing while thinking is
// off. What cannot be ranked still must not reach the upstream, but the
// ceiling is not what replaces it: that would switch reasoning on.
func TestEffortCeilingDoesNotSwitchOnAControlThatIsOff(t *testing.T) {
	for name, control := range map[string]string{
		"review request: a repeated type":                `"thinking":{"type":"enabled","budget_tokens":32000,"type":"disabled"}`,
		"a re-cased type before":                         `"thinking":{"Type":"enabled","budget_tokens":32000,"type":"disabled"}`,
		"a re-cased type after":                          `"thinking":{"type":"enabled","budget_tokens":32000,"TYPE":"disabled"}`,
		"review request: a budget beside a switch off":   `"chat_template_kwargs":{"enable_thinking":false,"thinking_budget":8192}`,
		"a budget beside a switch off, at the top level": `"enable_thinking":false,"thinking_budget":8192`,
		"a repeated template switch that ends off":       `"chat_template_kwargs":{"enable_thinking":true,"enable_thinking":false}`,
		"a repeated control that ends off":               `"extra_body":{"thinking":{"type":"enabled","budget_tokens":32000},"thinking":{"type":"disabled"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, "/low", "/v1/chat/completions", `{"model":"openai/gpt-5.1",`+hiMessages+`,`+control+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, "openai")
			assert.Equal(t, "none", openAIEffort(sent))
			for key := range sent {
				if key != "reasoning_effort" {
					assert.NotContains(t, reasoningMembers, strings.ToLower(key), "%s reached the upstream", key)
				}
			}
		})
	}
}

// A re-cased control within the ceiling is no reason to rewrite the request.
func TestEffortCeilingKeepsRecasedControlsWithinIt(t *testing.T) {
	f := newUpstreamFixture(t, cappedRows, nil)
	rec := f.post(t, "/low", "/v1/chat/completions", `{"model":"openai/gpt-4o",`+hiMessages+`,"Enable_Thinking":false,"Think":"low"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sent := f.sent(t, "openai")
	assert.Equal(t, false, sent["Enable_Thinking"])
	assert.Equal(t, "low", sent["Think"])
	assert.NotContains(t, sent, "reasoning_effort", "a ceiling adds no effort")
}

// Gemini's decoder merges repeated objects, so a thinking budget in any
// occurrence of extra_body, google or thinking_config is one it sends.
func TestEffortCeilingReadsEveryOccurrenceOfGeminiThinkingConfig(t *testing.T) {
	const budget = `{"google":{"thinking_config":{"thinking_budget":32768}}}`
	for name, extraBody := range map[string]string{
		"review request: a null extra_body after": `"extra_body":` + budget + `,"extra_body":null`,
		"an extra_body without a budget after":    `"extra_body":` + budget + `,"extra_body":{"google":{"thinking_config":{"include_thoughts":true}}}`,
		"an extra_body without a budget before":   `"extra_body":{"google":{"thinking_config":{"include_thoughts":true}}},"extra_body":` + budget,
		"a repeated google":                       `"extra_body":{"google":{"thinking_config":{"thinking_budget":32768}},"google":{}}`,
		"a repeated thinking_config":              `"extra_body":{"google":{"thinking_config":{"thinking_budget":32768},"thinking_config":{"include_thoughts":true}}}`,
		"a repeated budget":                       `"extra_body":{"google":{"thinking_config":{"thinking_budget":32768,"thinking_budget":1}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, "/low", "/v1/chat/completions", `{"model":"gemini/gemini-2.5-flash",`+hiMessages+`,`+extraBody+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			thinking := object(t, object(t, f.sent(t, "gemini"), "generationConfig"), "thinkingConfig")
			assert.Equal(t, 1024, number(t, thinking, "thinkingBudget"), "the low level's budget, not the client's")
		})
	}
}
