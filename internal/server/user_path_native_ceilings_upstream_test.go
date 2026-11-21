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
