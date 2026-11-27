package zai

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Z.ai reports "model_context_window_exceeded" and "sensitive" where OpenAI
// clients expect "length" and "content_filter".
var finishReasonCases = []struct {
	upstream, want, status, incomplete string
}{
	{"model_context_window_exceeded", "length", "incomplete", "max_output_tokens"},
	{"sensitive", "content_filter", "incomplete", "content_filter"},
}

func TestMapsFinishReasons(t *testing.T) {
	for _, tt := range finishReasonCases {
		t.Run(tt.upstream, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"c1","object":"chat.completion","created":1,"model":"glm-5.3","choices":[{"index":0,"message":{"role":"assistant","content":"cut"},"finish_reason":"`+tt.upstream+`"}]}`)
			provider := newTestProvider("zai-key", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "glm-5.3",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			require.Len(t, resp.Choices, 1)
			assert.Equal(t, tt.want, resp.Choices[0].FinishReason)

			responses, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "glm-5.3", Input: "hi"})
			require.NoError(t, err)
			assert.Equal(t, tt.status, responses.Status)
			require.NotNil(t, responses.IncompleteDetails)
			assert.Equal(t, tt.incomplete, responses.IncompleteDetails.Reason)
		})
	}
}

func TestStreamMapsFinishReasons(t *testing.T) {
	for _, tt := range finishReasonCases {
		t.Run(tt.upstream, func(t *testing.T) {
			server, _ := providertest.SSEServer(t, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cut\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\""+tt.upstream+"\"}]}\n\n"+
				"data: [DONE]\n\n")
			provider := newTestProvider("zai-key", server.URL, server.Client(), llmclient.Hooks{})

			stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "glm-5.3",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
				Stream:   true,
			})
			require.NoError(t, err)
			body, err := io.ReadAll(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			assert.Contains(t, string(body), `"finish_reason":"`+tt.want+`"`)
			assert.NotContains(t, string(body), tt.upstream)
			assert.Contains(t, string(body), `"content":"cut"`)
			assert.Contains(t, string(body), "data: [DONE]")

			stream, err = provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "glm-5.3", Input: "hi"})
			require.NoError(t, err)
			body, err = io.ReadAll(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			assert.Contains(t, string(body), "event: response.incomplete")
			assert.Contains(t, string(body), `"reason":"`+tt.incomplete+`"`)
		})
	}
}

func TestMapFinishReasonsLeavesOtherPayloadsUntouched(t *testing.T) {
	for _, in := range []string{
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"network_error"}]}`,
		`{"choices":[{"index":0,"delta":{"content":"sensitive"},"finish_reason":null}]}`,
	} {
		out, changed := mapFinishReasons([]byte(in))
		assert.False(t, changed)
		assert.Equal(t, in, string(out))
	}
}
