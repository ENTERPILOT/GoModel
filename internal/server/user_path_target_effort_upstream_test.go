package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// targetEffortModels force an effort on their targets, next to the two
// efforts a target's own outranks or yields to: the virtual model's, and a
// policy on the concrete model.
var targetEffortModels = []virtualmodels.VirtualModel{
	{
		Source: "team", Strategy: virtualmodels.StrategyFailover, ReasoningEffort: "medium", Enabled: true,
		Targets: []virtualmodels.Target{
			{Model: "openai/gpt-5.1", ReasoningEffort: "high"},
			{Model: "anthropic/claude-opus-5", ReasoningEffort: "xhigh"},
		},
	},
	{
		Source: "inherits", ReasoningEffort: "medium", Enabled: true,
		Targets: []virtualmodels.Target{{Model: "openai/gpt-5.1"}},
	},
	{
		Source: "pinned", Enabled: true,
		Targets: []virtualmodels.Target{{Model: "openai/gpt-5", ReasoningEffort: "high"}},
	},
	{Source: "openai/gpt-5", ProviderName: "openai", Model: "gpt-5", ReasoningEffort: "medium", Enabled: true},
	{
		Source: "always", Enabled: true,
		Targets: []virtualmodels.Target{{Model: "anthropic/claude-fable-5-2", ReasoningEffort: "high"}},
	},
}

// The effort a virtual model sets on a target is forced like its own: it
// replaces the client's effort and the virtual model's, and yields to a
// policy on the concrete model. Whichever of them wins, the caller's effort
// ceiling applies last, on the first attempt and on a failover leg, and
// never raises an effort below it.
func TestCeilingBoundsAVirtualModelTargetEffort(t *testing.T) {
	const client = `,"reasoning_effort":"max",`
	tests := []struct {
		name     string
		userPath string
		model    string
		failing  []string
		// wantOpenAI is the effort the OpenAI upstream gets, wantAnthropic
		// the output_config.effort the Anthropic one gets: "" when the
		// attempt is not expected there.
		wantOpenAI, wantAnthropic string
	}{
		{name: "without a ceiling the target's effort beats the client's and the virtual model's", model: "team", wantOpenAI: "high"},
		{name: "review request: a target effort above the ceiling is lowered", userPath: "/low", model: "team", wantOpenAI: "low"},
		{name: "a ceiling between the efforts", userPath: "/medium", model: "team", wantOpenAI: "medium"},
		{name: "a ceiling at the target's effort", userPath: "/high", model: "team", wantOpenAI: "high"},
		{name: "without a ceiling a failover leg takes its own target's effort", model: "team", failing: []string{"openai"}, wantOpenAI: "high", wantAnthropic: "xhigh"},
		{name: "review request: a failover leg to an adaptive-thinking target is lowered", userPath: "/low", model: "team", failing: []string{"openai"}, wantOpenAI: "low", wantAnthropic: "low"},
		{name: "a ceiling above the first leg's effort and below the second's", userPath: "/high", model: "team", failing: []string{"openai"}, wantOpenAI: "high", wantAnthropic: "high"},
		{name: "a target without an effort takes the virtual model's", model: "inherits", wantOpenAI: "medium"},
		{name: "the virtual model's effort is lowered", userPath: "/low", model: "inherits", wantOpenAI: "low"},
		{name: "a policy on the concrete model beats the target's effort", model: "pinned", wantOpenAI: "medium"},
		{name: "the policy's effort is lowered", userPath: "/low", model: "pinned", wantOpenAI: "low"},
		{name: "a ceiling above the policy's effort does not raise it to the target's", userPath: "/high", model: "pinned", wantOpenAI: "medium"},
		{name: "a target that always thinks gets the least it accepts under a none ceiling", userPath: "/off", model: "always", wantAnthropic: "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, targetEffortModels, tt.failing...)
			rec := f.post(t, tt.userPath, "/v1/chat/completions", `{"model":"`+tt.model+`"`+client+hiMessages+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			if tt.wantOpenAI == "" {
				assert.Zero(t, f.upstreams["openai"].Count())
			} else {
				assert.Equal(t, tt.wantOpenAI, openAIEffort(f.sent(t, "openai")))
			}
			if tt.wantAnthropic == "" {
				assert.Zero(t, f.upstreams["anthropic"].Count())
				return
			}
			sent := f.sent(t, "anthropic")
			assert.Equal(t, "adaptive", object(t, sent, "thinking")["type"])
			assert.Equal(t, tt.wantAnthropic, object(t, sent, "output_config")["effort"])
		})
	}
}

// A target's effort is forced on Messages requests too, which then take the
// translated pipeline instead of being forwarded as sent, and on the
// Responses API. A none ceiling turns the thinking it would switch on off.
func TestCeilingBoundsATargetEffortInEveryDialect(t *testing.T) {
	const adaptive = `"max_tokens":2048,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},`
	models := []virtualmodels.VirtualModel{{
		Source: "claude", Enabled: true,
		Targets: []virtualmodels.Target{{Model: "anthropic/claude-opus-5", ReasoningEffort: "high"}},
	}}
	tests := []struct {
		name     string
		userPath string
		path     string
		body     string
		// wantThinking is the thinking type sent and wantEffort the
		// output_config.effort: "" for none.
		wantThinking, wantEffort string
	}{
		{name: "messages without a ceiling", path: "/v1/messages", body: `{"model":"claude",` + adaptive + hiMessages + `}`, wantThinking: "adaptive", wantEffort: "high"},
		{name: "messages under a low ceiling", userPath: "/low", path: "/v1/messages", body: `{"model":"claude",` + adaptive + hiMessages + `}`, wantThinking: "adaptive", wantEffort: "low"},
		{name: "messages that asked for no thinking", userPath: "/low", path: "/v1/messages", body: `{"model":"claude","max_tokens":2048,` + hiMessages + `}`, wantThinking: "adaptive", wantEffort: "low"},
		{name: "responses under a low ceiling", userPath: "/low", path: "/v1/responses", body: `{"model":"claude","input":"hi"}`, wantThinking: "adaptive", wantEffort: "low"},
		{name: "chat under a none ceiling", userPath: "/off", path: "/v1/chat/completions", body: `{"model":"claude",` + hiMessages + `}`, wantThinking: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, models)
			rec := f.post(t, tt.userPath, tt.path, tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, "anthropic")
			assert.Equal(t, tt.wantThinking, object(t, sent, "thinking")["type"])
			if tt.wantEffort == "" {
				assert.NotContains(t, sent, "output_config")
				return
			}
			assert.Equal(t, tt.wantEffort, object(t, sent, "output_config")["effort"])
		})
	}
}
