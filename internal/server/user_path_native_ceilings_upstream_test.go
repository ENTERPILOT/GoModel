package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverToolHistory is a conversation only Anthropic understands: the
// translated pipeline refuses it, so it can only be forwarded natively.
const serverToolHistory = `"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search"},` +
	`{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]},` +
	`{"role":"user","content":"and?"}]`

// A Messages request that has no translated path is not refused above its
// caller's ceilings: the limits are lowered in the body itself, and every
// other byte reaches Anthropic as sent.
func TestCeilingsLowerANativeOnlyMessagesBodyInPlace(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		limits   string
		want     string
		added    string // a member the body did not have goes first
	}{
		{
			name:     "review request: max_tokens above the cap",
			userPath: "/tokens",
			limits:   `"max_tokens":64000`,
			want:     `"max_tokens":1000`,
		},
		{
			name:     "within the ceilings nothing changes",
			userPath: "/capped",
			limits:   `"max_tokens": 1000, "thinking":{"type":"disabled"}`,
			want:     `"max_tokens": 1000, "thinking":{"type":"disabled"}`,
		},
		{
			name:     "a thinking budget above the ceiling level's own",
			userPath: "/low",
			limits:   `"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":30000,"display":"summarized"}`,
			want:     `"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":5000,"display":"summarized"}`,
		},
		{
			name:     "a budget shrinks to fit under the capped max_tokens",
			userPath: "/roomy",
			limits:   `"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":16000}`,
			want:     `"max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":6976}`,
		},
		{
			name:     "thinking is turned off when no budget fits",
			userPath: "/capped",
			limits:   `"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":4000}`,
			want:     `"max_tokens":1000,"thinking":{"type":"disabled"}`,
		},
		{
			name:     "a none ceiling turns thinking off",
			userPath: "/off",
			limits:   `"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024}`,
			want:     `"max_tokens":2048,"thinking":{"type":"disabled"}`,
		},
		{
			name:     "an effort above the ceiling",
			userPath: "/low",
			limits:   `"max_tokens":2048,"output_config":{"effort":"max","format":{"type":"json_schema","schema":{"type":"object"}}}`,
			want:     `"max_tokens":2048,"output_config":{"effort":"low","format":{"type":"json_schema","schema":{"type":"object"}}}`,
		},
		{
			name:     "adaptive thinking gets the ceiling as its effort",
			userPath: "/low",
			limits:   `"max_tokens":2048,"thinking":{"type":"adaptive"}`,
			want:     `"max_tokens":2048,"thinking":{"type":"adaptive"}`,
			added:    `"output_config":{"effort":"low"},`,
		},
		{
			name:     "adaptive thinking that names no effort runs at high upstream",
			userPath: "/medium",
			limits:   `"max_tokens":2048,"thinking":{"type":"adaptive"}`,
			want:     `"max_tokens":2048,"thinking":{"type":"adaptive"}`,
			added:    `"output_config":{"effort":"medium"},`,
		},
		{
			name:     "adaptive thinking has no budget to fit under the cap",
			userPath: "/capped",
			limits:   `"max_tokens":64000,"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"xhigh"}`,
			want:     `"max_tokens":1000,"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"low"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, tt.userPath, "/v1/messages", `{"model":"anthropic/claude-sonnet-4-5",`+tt.limits+`,`+serverToolHistory+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, `{`+tt.added+`"model":"claude-sonnet-4-5",`+tt.want+`,`+serverToolHistory+`}`, string(f.upstreams["anthropic"].Last(t).Body))
		})
	}
}

// effortTurns is a conversation whose system entry sets the effort of the
// turns after it, Anthropic's per-message effort.
func effortTurns(effort string) string {
	return `"messages":[{"role":"user","content":"hi"},{"role":"system","content":[],"output_config":{"effort":"` + effort + `"}},{"role":"user","content":"more"}]`
}

// A body within its ceilings at the top level is forwarded as it was sent,
// so an effort one of its messages sets is lowered where it stands: the
// top-level effort is not the only one Anthropic reads.
func TestCeilingLowersAPerMessageEffortOfANativeMessagesBody(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		limits   string
		sent     string
		want     string
	}{
		{name: "review request: max in a message under a low ceiling", userPath: "/low", limits: `"output_config":{"effort":"low"}`, sent: "max", want: "low"},
		{name: "no top-level effort at all", userPath: "/medium", limits: `"temperature":1`, sent: "xhigh", want: "medium"},
		{name: "within the ceiling nothing changes", userPath: "/high", limits: `"output_config":{"effort":"high"}`, sent: "medium", want: "medium"},
		{name: "a token ceiling bounds no effort", userPath: "/tokens", limits: `"output_config":{"effort":"max"}`, sent: "max", want: "max"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, tt.userPath, "/v1/messages", `{"model":"anthropic/claude-opus-5","max_tokens":512,`+tt.limits+`,`+effortTurns(tt.sent)+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, `{"model":"claude-opus-5","max_tokens":512,`+tt.limits+`,`+effortTurns(tt.want)+`}`, string(f.upstreams["anthropic"].Last(t).Body))
		})
	}

	t.Run("with a top-level effort above the ceiling in a native-only body", func(t *testing.T) {
		const history = `{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]}`
		body := func(effort string) string {
			return `"max_tokens":512,"output_config":{"effort":"` + effort + `"},"tools":[{"type":"web_search_20250305","name":"web_search"}],` +
				`"messages":[{"role":"user","content":"search"},` + history + `,{"role":"system","content":[],"output_config":{"effort":"` + effort + `"}},{"role":"user","content":"and?"}]}`
		}
		f := newUpstreamFixture(t, cappedRows, nil)
		rec := f.post(t, "/low", "/v1/messages", `{"model":"anthropic/claude-opus-5",`+body("max"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		assert.Equal(t, `{"model":"claude-opus-5",`+body("low"), string(f.upstreams["anthropic"].Last(t).Body))
	})

	t.Run("with a top-level effort above the ceiling in a body the pipeline could carry", func(t *testing.T) {
		f := newUpstreamFixture(t, cappedRows, nil)
		rec := f.post(t, "/low", "/v1/messages", `{"model":"anthropic/claude-opus-5","max_tokens":512,"output_config":{"effort":"max"},`+effortTurns("max")+`}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		assert.Equal(t, `{"model":"claude-opus-5","max_tokens":512,"output_config":{"effort":"low"},`+effortTurns("low")+`}`, string(f.upstreams["anthropic"].Last(t).Body))
	})
}

// Claude Opus 4.6 and Sonnet 4.6 take effort "max" but answer 400 for
// "xhigh", so under an xhigh ceiling they are sent "high", the next level
// down they take, on every path a request reaches them by.
func TestXHighCeilingSendsHighToAModelWithoutThatLevel(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "chat completions", path: "/v1/chat/completions", body: `{"model":"anthropic/claude-opus-4-6","reasoning_effort":"max",` + hiMessages + `}`, want: "high"},
		{name: "messages, lowered in the forwarded body", path: "/v1/messages", body: `{"model":"anthropic/claude-opus-4-6","max_tokens":512,"output_config":{"effort":"max"},` + hiMessages + `}`, want: "high"},
		{name: "messages, lowered in a native-only body", path: "/v1/messages", body: `{"model":"anthropic/claude-opus-4-6","max_tokens":512,"output_config":{"effort":"max"},` + serverToolHistory + `}`, want: "high"},
		{name: "messages within the ceiling", path: "/v1/messages", body: `{"model":"anthropic/claude-opus-4-6","max_tokens":512,"output_config":{"effort":"high"},` + hiMessages + `}`, want: "high"},
		{name: "sonnet 4.6, chat completions", path: "/v1/chat/completions", body: `{"model":"anthropic/claude-sonnet-4-6","reasoning_effort":"max",` + hiMessages + `}`, want: "high"},
		{name: "sonnet 4.6, messages lowered in the forwarded body", path: "/v1/messages", body: `{"model":"anthropic/claude-sonnet-4-6","max_tokens":512,"output_config":{"effort":"max"},` + hiMessages + `}`, want: "high"},
		{name: "sonnet 4.6, messages lowered in a native-only body", path: "/v1/messages", body: `{"model":"anthropic/claude-sonnet-4-6","max_tokens":512,"output_config":{"effort":"max"},` + serverToolHistory + `}`, want: "high"},
		{name: "a model with the level gets it", path: "/v1/chat/completions", body: `{"model":"anthropic/claude-opus-5","reasoning_effort":"max",` + hiMessages + `}`, want: "xhigh"},
		{name: "a model with the level gets it in a native-only body", path: "/v1/messages", body: `{"model":"anthropic/claude-opus-5","max_tokens":512,"output_config":{"effort":"max"},` + serverToolHistory + `}`, want: "xhigh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpstreamFixture(t, cappedRows, nil)
			rec := f.post(t, "/xhigh", tt.path, tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, tt.want, object(t, f.sent(t, "anthropic"), "output_config")["effort"])
		})
	}
}
