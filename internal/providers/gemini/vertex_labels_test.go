package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const (
	nativeReply = `{"responseId":"r","candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`
	compatReply = `{"id":"r","object":"chat.completion","model":"gemini-4-argon","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
)

// Labels the gateway cannot send must stop the provider: dropping them would
// let calls go unattributed in Cloud Billing.
func TestNewVertex_LabelsNeedNativeMode(t *testing.T) {
	labels := map[string]string{"code-review": "private"}
	tests := []struct {
		name          string
		apiMode       string
		labels        map[string]string
		reply         string
		wantConfigErr bool
	}{
		{name: "native with labels", apiMode: "native", labels: labels, reply: nativeReply},
		{name: "openai_compatible with labels", apiMode: "openai_compatible", labels: labels, wantConfigErr: true},
		{name: "openai_compatible without labels", apiMode: "openai_compatible", reply: compatReply},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, tt.reply)
			provider := NewVertexWithHTTPClient(providers.ProviderConfig{
				BaseURL:      server.URL + "/publishers/google",
				APIMode:      tt.apiMode,
				VertexLabels: tt.labels,
			}, providertest.Options(llmclient.Hooks{}), server.Client())

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "gemini-4-argon",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})

			if tt.wantConfigErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "vertex_labels requires api_mode native")
				assert.Equal(t, 0, capture.Count())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, capture.Count())
		})
	}
}

// Every generateContent call a labeled Vertex provider makes carries the
// labels: chat, streamed chat, and Gemini image models.
func TestVertex_SendsLabelsOnGenerateContent(t *testing.T) {
	labels := map[string]string{"code-review": "private"}
	chatRequest := &core.ChatRequest{Model: "gemini-4-argon", Messages: []core.Message{{Role: "user", Content: "hi"}}}
	newProvider := func(server *httptest.Server) *Provider {
		return NewVertexWithHTTPClient(providers.ProviderConfig{
			BaseURL:      server.URL + "/publishers/google",
			APIMode:      "native",
			VertexLabels: labels,
		}, providertest.Options(llmclient.Hooks{}), server.Client())
	}

	t.Run("chat", func(t *testing.T) {
		server, capture := providertest.JSONServer(t, http.StatusOK, nativeReply)
		_, err := newProvider(server).ChatCompletion(context.Background(), chatRequest)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"code-review": "private"}, capture.Last(t).JSON(t)["labels"])
	})
	t.Run("streamed chat", func(t *testing.T) {
		server, capture := providertest.SSEServer(t, "data: "+nativeReply+"\n\n")
		stream, err := newProvider(server).StreamChatCompletion(context.Background(), chatRequest)
		require.NoError(t, err)
		_, err = io.ReadAll(stream)
		require.NoError(t, err)
		require.NoError(t, stream.Close())
		assert.Equal(t, map[string]any{"code-review": "private"}, capture.Last(t).JSON(t)["labels"])
	})
	t.Run("gemini image model", func(t *testing.T) {
		server, capture := providertest.JSONServer(t, http.StatusOK,
			`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aW1n"}}]},"finishReason":"STOP"}]}`)
		_, err := newProvider(server).CreateImage(context.Background(), decodeImageRequest(t, `{"model":"gemini-2.5-flash-image","prompt":"x"}`))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"code-review": "private"}, capture.Last(t).JSON(t)["labels"])
	})
}

// Labels go only where they were configured and are accepted: an AI Studio
// provider ignores vertex_labels, and an unlabeled Vertex provider sends none.
func TestGenerateContent_NoLabelsMember(t *testing.T) {
	labels := map[string]string{"code-review": "private"}
	tests := []struct {
		name        string
		newProvider func(server *httptest.Server) *Provider
	}{
		{
			name: "ai studio with vertex_labels",
			newProvider: func(server *httptest.Server) *Provider {
				opts := providertest.Options(llmclient.Hooks{})
				opts.HTTPClient = server.Client()
				p := New(providers.ProviderConfig{APIKey: "key", APIMode: "native", VertexLabels: labels}, opts).(*Provider)
				p.SetModelsURL(server.URL)
				return p
			},
		},
		{
			name: "vertex without labels",
			newProvider: func(server *httptest.Server) *Provider {
				return NewVertexWithHTTPClient(providers.ProviderConfig{BaseURL: server.URL + "/publishers/google", APIMode: "native"},
					providertest.Options(llmclient.Hooks{}), server.Client())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, nativeReply)
			_, err := tt.newProvider(server).ChatCompletion(context.Background(),
				&core.ChatRequest{Model: "gemini-4-argon", Messages: []core.Message{{Role: "user", Content: "hi"}}})
			require.NoError(t, err)
			assert.NotContains(t, capture.Last(t).JSON(t), "labels")
		})
	}
}
