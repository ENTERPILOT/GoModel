package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Adaptive thinking carries its effort in output_config and has no budget:
// max_tokens bounds what it spends. Under the ceilings the limit is the cap
// and the effort the ceiling, thinking is not turned off for want of a
// budget, and a model that thinks on its own gets no effort it did not send.
func TestCeilingsHoldForAdaptiveThinking(t *testing.T) {
	const adaptive = `"thinking":{"type":"adaptive"}`
	tests := []struct {
		name     string
		userPath string
		path     string
		body     string
		// wantTokens is the max_tokens sent, 0 for the adapter's default.
		wantTokens int
		// wantThinking is the thinking type sent and wantEffort the
		// output_config.effort: "" for none.
		wantThinking string
		wantEffort   string
	}{
		{
			name: "chat: the effort is the ceiling and the limit the cap", userPath: "/capped", path: "/v1/chat/completions",
			body:       `{"model":"anthropic/claude-opus-5","max_tokens":64000,"reasoning_effort":"high",` + hiMessages + `}`,
			wantTokens: 1000, wantThinking: "adaptive", wantEffort: "low",
		},
		{
			name: "chat: a model that thinks by default gets no effort it did not send", userPath: "/capped", path: "/v1/chat/completions",
			body:       `{"model":"anthropic/claude-opus-5",` + hiMessages + `}`,
			wantTokens: 1000,
		},
		{
			name: "chat: a none ceiling turns thinking off where the model allows it", userPath: "/off", path: "/v1/chat/completions",
			body:         `{"model":"anthropic/claude-opus-5","reasoning_effort":"high",` + hiMessages + `}`,
			wantThinking: "disabled",
		},
		{
			name: "chat: a model that always thinks gets the least it accepts", userPath: "/off", path: "/v1/chat/completions",
			body:         `{"model":"anthropic/claude-fable-5-2","reasoning_effort":"high",` + hiMessages + `}`,
			wantThinking: "adaptive", wantEffort: "low",
		},
		{
			name: "responses: a summary keeps thinking within the ceilings", userPath: "/capped", path: "/v1/responses",
			body:       `{"model":"anthropic/claude-fable-5-2","input":"hi","reasoning":{"effort":"xhigh","summary":"auto"}}`,
			wantTokens: 1000, wantThinking: "adaptive", wantEffort: "low",
		},
		{
			name: "messages: an effort above the ceiling", userPath: "/low", path: "/v1/messages",
			body:       `{"model":"anthropic/claude-opus-5","max_tokens":2048,` + adaptive + `,"output_config":{"effort":"max"},` + hiMessages + `}`,
			wantTokens: 2048, wantThinking: "adaptive", wantEffort: "low",
		},
		{
			name: "messages: no effort runs at high upstream, above a medium ceiling", userPath: "/medium", path: "/v1/messages",
			body:       `{"model":"anthropic/claude-opus-5","max_tokens":2048,` + adaptive + `,` + hiMessages + `}`,
			wantTokens: 2048, wantThinking: "adaptive", wantEffort: "medium",
		},
		{
			name: "messages: no effort within a high ceiling is forwarded as sent", userPath: "/high", path: "/v1/messages",
			body:       `{"model":"anthropic/claude-opus-5","max_tokens":2048,` + adaptive + `,` + hiMessages + `}`,
			wantTokens: 2048, wantThinking: "adaptive",
		},
		{
			name: "messages: a model that thinks by default gets no effort it did not send", userPath: "/capped", path: "/v1/messages",
			body:       `{"model":"anthropic/claude-opus-5","max_tokens":64000,` + hiMessages + `}`,
			wantTokens: 1000,
		},
		{
			name: "native-only messages: lowered in the body", userPath: "/capped", path: "/v1/messages",
			body:       `{"model":"anthropic/claude-fable-5-2","max_tokens":64000,` + adaptive + `,"output_config":{"effort":"xhigh"},` + serverToolHistory + `}`,
			wantTokens: 1000, wantThinking: "adaptive", wantEffort: "low",
		},
		{
			name: "native-only messages: a model that thinks by default gets no effort it did not send", userPath: "/capped", path: "/v1/messages",
			body:       `{"model":"anthropic/claude-opus-5","max_tokens":64000,` + serverToolHistory + `}`,
			wantTokens: 1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, tt.userPath, tt.path, tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			sent := f.sent(t, "anthropic")
			if tt.wantTokens == 0 {
				tt.wantTokens = 4096
			}
			assert.Equal(t, tt.wantTokens, number(t, sent, "max_tokens"))
			if tt.wantThinking == "" {
				assert.NotContains(t, sent, "thinking")
			} else {
				thinking := object(t, sent, "thinking")
				assert.Equal(t, tt.wantThinking, thinking["type"])
				assert.NotContains(t, thinking, "budget_tokens")
			}
			if tt.wantEffort == "" {
				assert.NotContains(t, sent, "output_config")
			} else {
				assert.Equal(t, tt.wantEffort, object(t, sent, "output_config")["effort"])
			}
		})
	}
}
