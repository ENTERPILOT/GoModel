//go:build live

package chatgpt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// TestLive_PromptCacheHits checks that the real Codex backend serves warm
// turns from its prompt cache. A request-shape test still passes if the
// backend ignores the cache signals; this one reads cached_tokens from usage.
//
// It calls chatgpt.com and spends subscription quota, so it runs only with
// the live tag and a token:
//
//	CHATGPT_API_KEY=$(jq -r .tokens.access_token ~/.codex/auth.json) \
//	  go test -tags live -run TestLive ./internal/providers/chatgpt/
//
// CHATGPT_LIVE_MODEL overrides the model (default gpt-5.6-terra).
func TestLive_PromptCacheHits(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("CHATGPT_API_KEY"))
	if token == "" {
		t.Skip("CHATGPT_API_KEY is not set")
	}
	model := os.Getenv("CHATGPT_LIVE_MODEL")
	if model == "" {
		model = "gpt-5.6-terra"
	}
	provider := newTestProvider(token, defaultBaseURL, &http.Client{Timeout: 2 * time.Minute}, llmclient.Hooks{})

	tests := []struct {
		name  string
		setup func(ctx context.Context, runID string) (context.Context, core.UnknownJSONFields)
	}{
		{
			name: "client prompt_cache_key",
			setup: func(ctx context.Context, runID string) (context.Context, core.UnknownJSONFields) {
				key, err := json.Marshal("pck_" + runID)
				require.NoError(t, err)
				return ctx, core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{promptCacheKeyField: key})
			},
		},
		{
			name: "detected session stands in for the key",
			setup: func(ctx context.Context, runID string) (context.Context, core.UnknownJSONFields) {
				return core.WithSessionID(ctx, "sess-"+runID), core.UnknownJSONFields{}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runID := randomID(t)
			ctx, extras := tt.setup(context.Background(), runID)
			instructions := coldPrefix(runID)

			for turn := 1; turn <= 3; turn++ {
				resp, err := provider.Responses(ctx, &core.ResponsesRequest{
					Model:        model,
					Instructions: instructions,
					Input:        fmt.Sprintf("Turn %d: reply with exactly ok.", turn),
					Reasoning:    &core.Reasoning{Effort: "low"},
					ExtraFields:  extras,
				})
				require.NoError(t, err)
				require.NotNil(t, resp.Usage)
				cached := 0
				if resp.Usage.PromptTokensDetails != nil {
					cached = resp.Usage.PromptTokensDetails.CachedTokens
				}
				t.Logf("turn %d: input=%d cached=%d", turn, resp.Usage.InputTokens, cached)
				if turn > 1 {
					assert.Positive(t, cached, "turn %d should be served from the prompt cache", turn)
				}
			}
		})
	}
}

// coldPrefix returns roughly 10k tokens of instructions unique to the run, so
// the first turn always starts on a cold cache.
func coldPrefix(runID string) string {
	var b strings.Builder
	b.WriteString("You are a terse assistant. Answer with one word.\n")
	for i := range 600 {
		fmt.Fprintf(&b, "Reference line %d for run %s: the quick brown fox jumps over the lazy dog.\n", i, runID)
	}
	return b.String()
}

func randomID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}
