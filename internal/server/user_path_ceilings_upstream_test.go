package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

const hiMessages = `"messages":[{"role":"user","content":"hi"}]`

// cappedRows are the user paths the upstream tests run under.
var cappedRows = []users.User{
	{UserPath: "/capped", MaxReasoningEffort: "low", MaxOutputTokens: 1000},
	{UserPath: "/roomy", MaxReasoningEffort: "high", MaxOutputTokens: 8000},
	{UserPath: "/high", MaxReasoningEffort: "high"},
	{UserPath: "/low", MaxReasoningEffort: "low"},
	{UserPath: "/off", MaxReasoningEffort: "none"},
	{UserPath: "/tokens", MaxOutputTokens: 1000},
}

func number(t *testing.T, body map[string]any, key string) int {
	t.Helper()
	value, ok := body[key].(float64)
	require.True(t, ok, "%s missing from upstream body %v", key, body)
	return int(value)
}

func object(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := body[key].(map[string]any)
	require.True(t, ok, "%s missing from upstream body %v", key, body)
	return value
}

// The Anthropic adapter raises max_tokens above a thinking budget and fills
// in a default when the client sends none. Under a token ceiling neither may
// exceed it: the budget shrinks to fit, or thinking is turned off when no
// valid budget fits.
func TestCeilingHoldsThroughAnthropicThinkingRoom(t *testing.T) {
	tests := []struct {
		name       string
		userPath   string
		path       string
		body       string
		wantTokens int
		wantBudget int // 0: no thinking block
	}{
		{
			name:       "review request: high effort under a 1000 cap and a low ceiling",
			userPath:   "/capped",
			path:       "/v1/chat/completions",
			body:       `{"model":"anthropic/claude-sonnet-4-5","max_tokens":1000,"reasoning_effort":"high",` + hiMessages + `}`,
			wantTokens: 1000,
		},
		{
			name:       "an omitted limit is the cap, not the adapter's default",
			userPath:   "/capped",
			path:       "/v1/chat/completions",
			body:       `{"model":"anthropic/claude-sonnet-4-5",` + hiMessages + `}`,
			wantTokens: 1000,
		},
		{
			name:       "the budget shrinks to fit under the cap",
			userPath:   "/roomy",
			path:       "/v1/chat/completions",
			body:       `{"model":"anthropic/claude-sonnet-4-5","max_tokens":8000,"reasoning_effort":"high",` + hiMessages + `}`,
			wantTokens: 8000,
			wantBudget: 8000 - 1024,
		},
		{
			name:       "Responses served by Anthropic",
			userPath:   "/capped",
			path:       "/v1/responses",
			body:       `{"model":"anthropic/claude-sonnet-4-5","input":"hi","reasoning":{"effort":"high"}}`,
			wantTokens: 1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, tt.userPath, tt.path, tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, "anthropic")
			assert.Equal(t, tt.wantTokens, number(t, sent, "max_tokens"))
			if tt.wantBudget == 0 {
				assert.NotContains(t, sent, "thinking")
				return
			}
			thinking := object(t, sent, "thinking")
			assert.Equal(t, "enabled", thinking["type"])
			assert.Equal(t, tt.wantBudget, number(t, thinking, "budget_tokens"))
		})
	}
}

// Native Messages forwarding sends the client's body verbatim, so an explicit
// thinking budget above the ceiling level's own budget must send the request
// through the translated pipeline, which sends that level's budget.
func TestCeilingBoundsNativeMessagesThinkingBudget(t *testing.T) {
	tests := []struct {
		name       string
		userPath   string
		body       string
		wantBudget int
	}{
		{
			name:       "review request: 63000 under a high ceiling",
			userPath:   "/high",
			body:       `{"model":"anthropic/claude-sonnet-4-5","max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":63000},` + hiMessages + `}`,
			wantBudget: 20000,
		},
		{
			name:       "review request: 9999 under a low ceiling",
			userPath:   "/low",
			body:       `{"model":"anthropic/claude-sonnet-4-5","max_tokens":16000,"thinking":{"type":"enabled","budget_tokens":9999},` + hiMessages + `}`,
			wantBudget: 5000,
		},
		{
			name:       "a budget within the ceiling is forwarded as sent",
			userPath:   "/low",
			body:       `{"model":"anthropic/claude-sonnet-4-5","max_tokens":16000,"thinking":{"type":"enabled","budget_tokens":2000},` + hiMessages + `}`,
			wantBudget: 2000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, tt.userPath, "/v1/messages", tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			thinking := object(t, f.sent(t, "anthropic"), "thinking")
			assert.Equal(t, tt.wantBudget, number(t, thinking, "budget_tokens"))
		})
	}
}

// Gemini decodes extra_body case-insensitively, so the ceiling must match
// the thinking config the same way.
func TestCeilingMatchesGeminiThinkingConfigCaseInsensitively(t *testing.T) {
	for name, extraBody := range map[string]string{
		"review request": `{"Google":{"Thinking_Config":{"thinking_budget":32768}}}`,
		"camel budget":   `{"GOOGLE":{"thinking_config":{"thinkingBudget":32768,"include_thoughts":true}}}`,
		"mixed level":    `{"google":{"THINKING_CONFIG":{"Thinking_Level":"high"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, "/low", "/v1/chat/completions", `{"model":"gemini/gemini-2.5-flash",`+hiMessages+`,"extra_body":`+extraBody+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			thinking := object(t, object(t, f.sent(t, "gemini"), "generationConfig"), "thinkingConfig")
			assert.Equal(t, 1024, number(t, thinking, "thinkingBudget"), "the low level's budget, not the client's")
			for key := range thinking {
				assert.Contains(t, []string{"thinkingBudget", "includeThoughts"}, key, "no client effort member may reach the upstream")
			}
		})
	}
}

// Provider-specific reasoning controls in extra fields reach upstreams
// verbatim, so under an effort ceiling they are replaced by the ceiling.
func TestCeilingReplacesProviderSpecificReasoningControls(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
		userPath string
		body     string
		absent   []string
		check    func(t *testing.T, sent map[string]any)
	}{
		{
			name:     "review request: Cohere thinking budget under a none ceiling",
			upstream: "cohere",
			userPath: "/off",
			body:     `{"model":"cohere/command-a-reasoning",` + hiMessages + `,"thinking":{"type":"enabled","token_budget":31999}}`,
			absent:   []string{"thinking"},
		},
		{
			name:     "OpenAI-compatible verbatim controls under a low ceiling",
			upstream: "openai",
			userPath: "/low",
			body: `{"model":"openai/gpt-4o",` + hiMessages + `,"thinking":{"type":"enabled"},"enable_thinking":true,"thinking_budget":32000,"think":"high",` +
				`"chat_template_kwargs":{"enable_thinking":true,"reasoning_effort":"max","add_generation_prompt":true}}`,
			absent: []string{"thinking", "enable_thinking", "thinking_budget", "think"},
			check: func(t *testing.T, sent map[string]any) {
				assert.Equal(t, map[string]any{"add_generation_prompt": true}, object(t, sent, "chat_template_kwargs"), "other template switches stay")
			},
		},
		{
			name:     "controls that turn reasoning off pass a none ceiling",
			upstream: "openai",
			userPath: "/off",
			body:     `{"model":"openai/gpt-4o",` + hiMessages + `,"enable_thinking":false,"thinking":{"type":"disabled"}}`,
			check: func(t *testing.T, sent map[string]any) {
				assert.Equal(t, false, sent["enable_thinking"])
				assert.Equal(t, map[string]any{"type": "disabled"}, sent["thinking"])
			},
		},
		{
			name:     "a repeated member cannot hide a second value",
			upstream: "openai",
			userPath: "/low",
			body:     `{"model":"openai/gpt-4o",` + hiMessages + `,"reasoning_effort":"low","reasoning_effort":"max"}`,
			check: func(t *testing.T, sent map[string]any) {
				assert.NotEqual(t, "max", sent["reasoning_effort"])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, tt.userPath, "/v1/chat/completions", tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, tt.upstream)
			for _, key := range tt.absent {
				assert.NotContains(t, sent, key)
			}
			if tt.check != nil {
				tt.check(t, sent)
			}
		})
	}
}

// OpenCode Zen injects a default effort into requests that set none; the
// injected effort stays within the caller's ceiling too.
func TestCeilingBoundsAnAdapterDefaultEffort(t *testing.T) {
	t.Setenv("OPENCODE_GO_DEFAULT_REASONING_EFFORT", "max")
	f := newUpstreamFixture(t, cappedRows, nil)

	rec := f.post(t, "/high", "/v1/chat/completions", `{"model":"opencode_go/kimi-k2-thinking",`+hiMessages+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "high", f.sent(t, "opencode_go")["reasoning_effort"])

	rec = f.post(t, "", "/v1/chat/completions", `{"model":"opencode_go/kimi-k2-thinking",`+hiMessages+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "max", f.sent(t, "opencode_go")["reasoning_effort"], "without a ceiling the operator's default stands")
}

// A ceiling on "/" binds every request: one with no user path at all, and
// one whose path an unbound key chose through the header.
func TestRootCeilingBindsRequestsWithAndWithoutAPath(t *testing.T) {
	rows := []users.User{{UserPath: "/", MaxReasoningEffort: "low", MaxOutputTokens: 1000}}
	for name, header := range map[string]string{"no user path": "", "header-chosen path": "/anything/i/like"} {
		t.Run(name, func(t *testing.T) {
			f := newUpstreamFixture(t, rows, nil)
			chain := RequestSnapshotCapture("")(AuthMiddleware("master-key", nil)(RequestBodyCapture()(f.resolve(f.handler.ChatCompletion))))
			options := []echotest.Option{echotest.WithHeader("Authorization", "Bearer master-key")}
			if header != "" {
				options = append(options, echotest.WithHeader(core.UserPathHeaderName(""), header))
			}
			c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"openai/gpt-5.1","max_tokens":32000,"reasoning_effort":"high",`+hiMessages+`}`, options...)
			require.NoError(t, chain(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, "openai")
			assert.Equal(t, 1000, openAILimit(t, sent))
			assert.Equal(t, "low", openAIEffort(sent))
		})
	}
}

// An omitted output token limit is set to the cap, in the field the dialect
// and model take upstream; an omitted effort stays omitted.
func TestTokenCeilingFillsAnOmittedLimitPerDialect(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		upstream string
		limit    func(t *testing.T, sent map[string]any) int
	}{
		{
			name: "chat to a reasoning model", path: "/v1/chat/completions", upstream: "openai",
			body: `{"model":"openai/gpt-5",` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int {
				assert.NotContains(t, sent, "max_tokens", "reasoning models take max_completion_tokens")
				return openAILimit(t, sent)
			},
		},
		{
			name: "chat to a classic model", path: "/v1/chat/completions", upstream: "openai",
			body:  `{"model":"openai/gpt-4o",` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_tokens") },
		},
		{
			name: "a null limit is no limit", path: "/v1/chat/completions", upstream: "openai",
			body:  `{"model":"openai/gpt-4o","max_completion_tokens":null,` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_tokens") },
		},
		{
			name: "responses", path: "/v1/responses", upstream: "openai",
			body:  `{"model":"openai/gpt-5","input":"hi"}`,
			limit: func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_output_tokens") },
		},
		{
			name: "messages translated to an OpenAI model", path: "/v1/messages", upstream: "openai",
			body: `{"model":"openai/gpt-4o","max_tokens":32000,` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int {
				return number(t, sent, "max_tokens")
			},
		},
		{
			name: "messages to Anthropic", path: "/v1/messages", upstream: "anthropic",
			body:  `{"model":"anthropic/claude-sonnet-4-5","max_tokens":32000,` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_tokens") },
		},
		{
			name: "chat to Gemini", path: "/v1/chat/completions", upstream: "gemini",
			body: `{"model":"gemini/gemini-2.5-flash",` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int {
				return number(t, object(t, sent, "generationConfig"), "maxOutputTokens")
			},
		},
		{
			name: "chat to Cohere", path: "/v1/chat/completions", upstream: "cohere",
			body:  `{"model":"cohere/command-a-reasoning",` + hiMessages + `}`,
			limit: func(t *testing.T, sent map[string]any) int { return number(t, sent, "max_tokens") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, "/tokens", tt.path, tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, tt.upstream)
			assert.Equal(t, 1000, tt.limit(t, sent))
			assert.NotContains(t, sent, "reasoning_effort", "a token cap adds no effort")
			assert.NotContains(t, sent, "reasoning", "a token cap adds no effort")
		})
	}
}

// Both ceilings hold when the dialect is translated and when a failover leg
// goes to another provider.
func TestCeilingsHoldAcrossTranslatedDialectsAndFailover(t *testing.T) {
	failover := []virtualmodels.VirtualModel{{
		Source:   "team",
		Strategy: virtualmodels.StrategyFailover,
		Targets:  []virtualmodels.Target{{Model: "openai/gpt-4o"}, {Model: "anthropic/claude-sonnet-4-5"}},
		Enabled:  true,
	}}

	t.Run("messages served by an OpenAI provider", func(t *testing.T) {
		f := newUpstreamFixture(t, cappedRows, nil)
		rec := f.post(t, "/capped", "/v1/messages", `{"model":"openai/gpt-5.1","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":30000},`+hiMessages+`}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		sent := f.sent(t, "openai")
		assert.Equal(t, 1000, openAILimit(t, sent))
		assert.Equal(t, "low", openAIEffort(sent))
	})

	for _, tt := range []struct {
		userPath   string
		wantTokens int
		wantBudget int
	}{
		{userPath: "/capped", wantTokens: 1000},
		{userPath: "/roomy", wantTokens: 8000, wantBudget: 8000 - 1024},
	} {
		t.Run("chat failing over to Anthropic under "+tt.userPath, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, failover, "openai")
			rec := f.post(t, tt.userPath, "/v1/chat/completions", `{"model":"team","max_tokens":64000,"reasoning_effort":"max",`+hiMessages+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sentToOpenAI := f.sent(t, "openai")
			assert.Equal(t, tt.wantTokens, number(t, sentToOpenAI, "max_tokens"), "the failed primary was capped too")
			sent := f.sent(t, "anthropic")
			assert.Equal(t, tt.wantTokens, number(t, sent, "max_tokens"))
			if tt.wantBudget == 0 {
				assert.NotContains(t, sent, "thinking")
				return
			}
			assert.Equal(t, tt.wantBudget, number(t, object(t, sent, "thinking"), "budget_tokens"))
		})
	}
}

// A Responses request continuing a stored response is capped like any other.
func TestCeilingsBoundAResponsesContinuation(t *testing.T) {
	f := newUpstreamFixture(t, cappedRows, nil)
	rec := f.post(t, "/capped", "/v1/responses", `{"model":"openai/gpt-5","input":"more","previous_response_id":"resp_prev","max_output_tokens":32000,"reasoning":{"effort":"high"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sent := f.sent(t, "openai")
	assert.Equal(t, "resp_prev", sent["previous_response_id"])
	assert.Equal(t, 1000, number(t, sent, "max_output_tokens"))
	assert.Equal(t, "low", object(t, sent, "reasoning")["effort"])
}

// A stored row with an effort outside the scale (written to the store
// directly, past validation) fails safe: the ceiling is enforced as "none"
// and the unknown value never reaches the upstream.
func TestStoredInvalidEffortCeilingFailsSafe(t *testing.T) {
	rows := []users.User{{UserPath: "/typo", MaxReasoningEffort: "turbo"}}
	f := newUpstreamFixture(t, rows, nil)

	rec := f.post(t, "/typo", "/v1/chat/completions", `{"model":"openai/gpt-5.1","reasoning_effort":"high",`+hiMessages+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "none", openAIEffort(f.sent(t, "openai")))
}
