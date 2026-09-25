package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionVideoInput(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "chat"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			if streaming {
				server, capture = providertest.SSEServer(t, providertest.ChatChunkSSE)
			}
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
			var req core.ChatRequest
			raw := `{"model":"MiniMax-M3","messages":[{"role":"user","content":[{"type":"text","text":"Describe this clip"},{"type":"video_url","video_url":{"url":"mm_file://clip","detail":"high","fps":2}}]}]}`
			require.NoError(t, json.Unmarshal([]byte(raw), &req))
			if streaming {
				body, err := provider.StreamChatCompletion(context.Background(), &req)
				require.NoError(t, err)
				defer body.Close()
				_, err = io.ReadAll(body)
				require.NoError(t, err)
			} else {
				_, err := provider.ChatCompletion(context.Background(), &req)
				require.NoError(t, err)
			}
			var expected map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &expected))
			require.Equal(t, expected["messages"], capture.Last(t).JSON(t)["messages"])
		})
	}
}
