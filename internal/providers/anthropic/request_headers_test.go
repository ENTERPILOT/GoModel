package anthropic

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestSetRequestHeaders_AddsHookHeadersToEveryRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	provider.SetRequestHeaders(func(ctx context.Context) http.Header {
		return http.Header{
			"X-Extra":    {"from-hook", "second"},
			"User-Agent": {"gomodel-test"},
		}
	})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	got := capture.Last(t).Header
	require.Equal(t, []string{"from-hook", "second"}, got.Values("X-Extra"))
	require.Equal(t, "gomodel-test", got.Get("User-Agent"), "hook replaces the default User-Agent")
	require.Equal(t, "test-api-key", got.Get("x-api-key"))
	require.NotEmpty(t, got.Get("anthropic-version"), "standard headers must survive the hook, got %v", got)
}
