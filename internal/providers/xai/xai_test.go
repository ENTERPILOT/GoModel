package xai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIKey = "test-api-key"

// newTestProvider builds a provider pointed at baseURL with the default client.
func newTestProvider(baseURL string) *Provider {
	return New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: baseURL}, providertest.Options(llmclient.Hooks{})).(*Provider)
}

// xAI forwards Responses to its native /responses endpoint and accepts an
// allowed_tools choice but ignores it, so the declared tools are narrowed.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "xai",
		DefaultBaseURL: "https://api.x.ai/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		NativeResponses: true,
		Embeddings:      true,
	})
}

func TestChatCompletion_ForwardsXGrokConvIDFromSnapshot(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{"x-grok-conv-id": {"client-conv-123"}},
		"application/json",
		nil,
		false,
		"req-123",
		nil,
	))
	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "grok-2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "client-conv-123", capture.Last(t).Header.Get(grokConvIDHeader))
}

func TestChatCompletion_GeneratesStableXGrokConvIDWhenMissing(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	initialMessages := []core.Message{
		{Role: "system", Content: "Reply with the requested marker only."},
		{Role: "user", Content: "Use this fixed reference text for the cache anchor."},
	}
	req1 := &core.ChatRequest{Model: "grok-2", Messages: initialMessages}
	req2 := &core.ChatRequest{
		Model: "grok-2",
		Messages: append(append([]core.Message{}, initialMessages...),
			core.Message{Role: "assistant", Content: "marker one"},
			core.Message{Role: "user", Content: "Now reply with marker two."},
		),
	}
	_, err := provider.ChatCompletion(context.Background(), req1)
	require.NoError(t, err)
	_, err = provider.ChatCompletion(context.Background(), req2)
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	first := requests[0].Header.Get(grokConvIDHeader)
	assert.True(t, strings.HasPrefix(first, "gomodel-"), "generated x-grok-conv-id = %q, want gomodel-*", first)
	assert.Equal(t, first, requests[1].Header.Get(grokConvIDHeader))
}

func TestStreamChatCompletion_ForwardsXGrokConvIDFromSnapshot(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: [DONE]\n\n")
	provider := newTestProvider(server.URL)

	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{"X-Grok-Conv-Id": {"stream-conv-123"}},
		"application/json",
		nil,
		false,
		"req-123",
		nil,
	))
	body, err := provider.StreamChatCompletion(ctx, &core.ChatRequest{
		Model:    "grok-2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	assert.Equal(t, "stream-conv-123", capture.Last(t).Header.Get(grokConvIDHeader))
}

func TestResponsesDropsMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		stream   bool
	}{
		{name: "non-streaming drops metadata", metadata: map[string]string{"team": "alpha"}},
		{name: "streaming drops metadata", metadata: map[string]string{"team": "alpha"}, stream: true},
		{name: "no metadata is a no-op", metadata: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"resp_1","object":"response","status":"completed"}`)
			provider := newTestProvider(server.URL)

			req := &core.ResponsesRequest{Model: "grok-4.3", Metadata: tt.metadata}
			var err error
			if tt.stream {
				var stream io.ReadCloser
				stream, err = provider.StreamResponses(context.Background(), req)
				if stream != nil {
					_ = stream.Close()
				}
			} else {
				_, err = provider.Responses(context.Background(), req)
			}
			require.NoError(t, err)
			assert.NotContains(t, capture.Last(t).JSON(t), "metadata")
			if len(tt.metadata) > 0 {
				assert.NotNil(t, req.Metadata, "caller request was mutated; metadata must survive for the client echo")
			}
		})
	}
}

func TestChatCompletion_MapsReasoningToXAIReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "grok-4.5",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "medium"},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "medium", sent["reasoning_effort"])
}

func TestChatCompletion_OmitsReasoningEffortWhenReasoningAbsent(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "grok-4.5",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning_effort")
	assert.NotContains(t, sent, "reasoning")
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		model  string
		effort string
		want   string
	}{
		{"grok-4.5", "low", "low"},
		{"grok-4.5", "medium", "medium"},
		{"grok-4.5", "high", "high"},
		{"grok-4.5", "none", "none"},
		{"grok-4.5", " High ", "high"},
		{"grok-4.5", "xhigh", "high"},
		{"grok-4.5", "max", "high"},
		{"grok-4.20-multi-agent-0309", "xhigh", "xhigh"},
		{"grok-4.20-multi-agent-0309", "max", "xhigh"},
		{"grok-4.20-multi-agent-0309", "low", "low"},
		{"grok-4.6", "xhigh", "xhigh"},
		{"grok-4.6", "max", "xhigh"},
		{"grok-4.6", "high", "high"},
		{"grok-4.6-latest", "xhigh", "xhigh"},
		{"grok-4.7", "xhigh", "xhigh"},
		{"grok-4.10", "xhigh", "xhigh"},
		{"grok-4.foo", "xhigh", "high"},
		{"grok-4.20-0309-reasoning", "xhigh", "high"},
		{"grok-4.20-0309-reasoning", "max", "high"},
		{"grok-4.3", "xhigh", "high"},
		{"grok-4.5", "custom-level", "custom-level"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, normalizeReasoningEffort(tt.model, tt.effort), "normalizeReasoningEffort(%q, %q)", tt.model, tt.effort)
	}
}

func TestChatCompletion_DropsReasoningEffortForModelsThatRejectIt(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "non-reasoning variant", model: "grok-4.20-0309-non-reasoning", effort: "medium"},
		{name: "reasoning variant keeps it", model: "grok-4.20-0309-reasoning", effort: "medium", wantEffort: "medium"},
		{name: "grok-build has a fixed effort", model: "grok-build-0.1", effort: "high"},
		{name: "grok-3 rejects it", model: "grok-3", effort: "high"},
		{name: "grok-3-mini takes it", model: "grok-3-mini", effort: "high", wantEffort: "high"},
		{name: "grok-2 rejects it", model: "grok-2-1212", effort: "low"},
		{name: "namespaced id", model: "xai/grok-4.20-0309-non-reasoning", effort: "low"},
		{name: "unknown models keep it", model: "grok-99-new", effort: "low", wantEffort: "low"},
		{name: "grok-4.5 keeps it", model: "grok-4.5", effort: "low", wantEffort: "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := newTestProvider(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.NotContains(t, sent, "reasoning")
			if tt.wantEffort == "" {
				assert.NotContains(t, sent, "reasoning_effort")
				return
			}
			assert.Equal(t, tt.wantEffort, sent["reasoning_effort"])
		})
	}
}

func TestChatCompletion_DropsEmptyReasoningObject(t *testing.T) {
	for _, effort := range []string{"", " \t "} {
		t.Run("effort="+effort, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := newTestProvider(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     "grok-4.5",
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: effort},
			})
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.NotContains(t, sent, "reasoning")
			assert.NotContains(t, sent, "reasoning_effort")
		})
	}
}
