package openrouter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestProvider builds a provider pointed at baseURL.
func newTestProvider(baseURL string, client *http.Client) *Provider {
	provider := NewWithHTTPClient("test-api-key", client, llmclient.Hooks{})
	provider.SetBaseURL(baseURL)
	return provider
}

// modelsByID indexes a listing by model ID.
func modelsByID(resp *core.ModelsResponse) map[string]core.Model {
	byID := map[string]core.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}
	return byID
}

// pricingOf returns the model's pricing, or nil when it carries none.
func pricingOf(m core.Model) any {
	if m.Metadata == nil || m.Metadata.Pricing == nil {
		return nil
	}
	return m.Metadata.Pricing
}

// ListModels must keep OpenRouter's architecture modalities and context
// length, mapping output modalities onto gateway modes so the catalog's long
// tail is categorized without remote-registry entries.
func TestListModels_StampsArchitectureModalities(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":[
		{"id":"openai/gpt-4o-mini","created":1721260800,"context_length":128000,
		 "architecture":{"input_modalities":["text","image"],"output_modalities":["text"]}},
		{"id":"google/gemini-3-pro-image","created":1721260800,
		 "architecture":{"input_modalities":["text"],"output_modalities":["image"]}},
		{"id":"voyageai/voyage-4-lite","created":1721260800,
		 "architecture":{"input_modalities":["text"],"output_modalities":["embeddings"]}},
		{"id":"fish-audio/s1","created":1721260800,
		 "architecture":{"input_modalities":["text"],"output_modalities":["speech"]}},
		{"id":"mistralai/voxtral-mini-3b-2507","created":1721260800,
		 "architecture":{"input_modalities":["audio"],"output_modalities":["transcription"]}},
		{"id":"cohere/rerank-only","created":1721260800,
		 "architecture":{"input_modalities":["text"],"output_modalities":["rerank"]}},
		{"id":"acme/video-only","created":1721260800,
		 "architecture":{"input_modalities":["text"],"output_modalities":["video"]}},
		{"id":"mystery/no-architecture","created":1721260800}
	]}`)
	provider := newTestProvider(server.URL, server.Client())

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/models", req.Path)
	// The endpoint defaults to text-output models; without this parameter
	// embedding models would never enter the catalog.
	assert.Equal(t, "all", req.Query.Get("output_modalities"))

	require.Len(t, resp.Data, 6)
	byID := modelsByID(resp)

	chat := byID["openai/gpt-4o-mini"]
	require.NotNil(t, chat.Metadata)
	assert.Equal(t, []string{"chat"}, chat.Metadata.Modes)
	require.NotNil(t, chat.Metadata.ContextWindow)
	assert.Equal(t, 128000, *chat.Metadata.ContextWindow)

	image := byID["google/gemini-3-pro-image"]
	require.NotNil(t, image.Metadata)
	assert.Equal(t, []string{"image_generation"}, image.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryImage}, image.Metadata.Categories)

	embed := byID["voyageai/voyage-4-lite"]
	require.NotNil(t, embed.Metadata)
	assert.Equal(t, []string{"embedding"}, embed.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryEmbedding}, embed.Metadata.Categories)

	speech := byID["fish-audio/s1"]
	require.NotNil(t, speech.Metadata)
	assert.Equal(t, []string{"audio_speech"}, speech.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryAudio}, speech.Metadata.Categories)

	stt := byID["mistralai/voxtral-mini-3b-2507"]
	require.NotNil(t, stt.Metadata)
	assert.Equal(t, []string{"audio_transcription"}, stt.Metadata.Modes)

	assert.NotContains(t, byID, "cohere/rerank-only")
	assert.NotContains(t, byID, "acme/video-only")

	noArch, ok := byID["mystery/no-architecture"]
	require.True(t, ok)
	assert.Nil(t, noArch.Metadata)
}

// A failed upstream listing must propagate as an error, not an empty catalog.
func TestListModels_UpstreamErrorPropagates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":{"message":"upstream exploded"}}`)
	provider := newTestProvider(server.URL, server.Client())

	_, err := provider.ListModels(context.Background())
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
}

// Audio flows through the embedded OpenAI-compatible implementation;
// OpenRouter-specific request mutation (attribution headers) must still apply
// on that path so audio traffic is attributed like every other call.
func TestAudio_UsesOpenAISurfaceWithAttributionHeaders(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/audio/speech": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("mp3-bytes"))
		},
		"/audio/transcriptions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello"}`))
		},
	})
	provider := newTestProvider(server.URL, server.Client())

	speech, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "fish-audio/s1",
		Input: "hello world",
		Voice: "alloy",
	})
	require.NoError(t, err)
	assert.Equal(t, "audio/mpeg", speech.ContentType)

	transcription, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "mistralai/voxtral-mini-3b-2507",
		File:     []byte("wav-bytes"),
		Filename: "clip.wav",
	})
	require.NoError(t, err)
	assert.Contains(t, string(transcription.Data), "hello")

	requests := capture.All()
	require.Len(t, requests, 2)
	for i, want := range []string{"/audio/speech", "/audio/transcriptions"} {
		assert.Equal(t, want, requests[i].Path)
		assert.Equal(t, defaultSiteURL, requests[i].Header.Get("HTTP-Referer"), "HTTP-Referer on %s", want)
		assert.Equal(t, defaultAppName, requests[i].Header.Get("X-OpenRouter-Title"), "X-OpenRouter-Title on %s", want)
	}
}

func TestChatCompletion_AddsDefaultAttributionHeaders(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL, server.Client())

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "openai/gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "Bearer test-api-key", req.Header.Get("Authorization"))
	assert.Equal(t, defaultSiteURL, req.Header.Get("HTTP-Referer"))
	assert.Equal(t, defaultAppName, req.Header.Get("X-OpenRouter-Title"))
}

func TestChatCompletion_ForwardsGoModelSessionID(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL, server.Client())

	ctx := core.WithSessionID(context.Background(), "conversation-42")
	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "openai/gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "conversation-42", capture.Last(t).Header.Get("X-Session-Id"))
}

func TestChatCompletion_UsesEnvOverridesForAttributionHeaders(t *testing.T) {
	t.Setenv("OPENROUTER_SITE_URL", "https://example.com")
	t.Setenv("OPENROUTER_APP_NAME", "Example App")

	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL, server.Client())

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "openai/gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "https://example.com", req.Header.Get("HTTP-Referer"))
	assert.Equal(t, "Example App", req.Header.Get("X-OpenRouter-Title"))
}

func TestPassthrough_PreservesUserProvidedAttributionHeaders(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusTooManyRequests, `{"error":"rate limited"}`)
	provider := newTestProvider(server.URL, server.Client())

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "responses",
		Body:     io.NopCloser(strings.NewReader(`{"model":"openai/gpt-4o-mini"}`)),
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"HTTP-Referer": {"https://caller.example"},
			"X-Title":      {"Caller App"},
		},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	req := capture.Last(t)
	assert.Equal(t, "https://caller.example", req.Header.Get("HTTP-Referer"))
	assert.Equal(t, "Caller App", req.Header.Get("X-Title"))
	assert.Empty(t, req.Header.Get("X-OpenRouter-Title"))
}

// OpenRouter prices its own catalog, including the ":free" variants it
// publishes at zero, so ListModels must carry those rates into metadata:
// enrichment treats what a provider reports as the override, and price-based
// model filtering and cost-based load balancing both read them.
func TestListModels_StampsPricing(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"data":[
		{"id":"openai/gpt-4o-mini","context_length":128000,
		 "architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"0.00000015","completion":"0.0000006"}},
		{"id":"deepseek/deepseek-r1:free",
		 "architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"0","completion":"0"}},
		{"id":"openrouter/auto",
		 "architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"-1","completion":"-1"}},
		{"id":"acme/unpriced","architecture":{"output_modalities":["text"]}}
	]}`)
	provider := newTestProvider(server.URL, server.Client())

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	byID := modelsByID(resp)

	paid := byID["openai/gpt-4o-mini"].Metadata
	require.NotNil(t, paid)
	require.NotNil(t, paid.Pricing)
	assert.Equal(t, "USD", paid.Pricing.Currency)

	// Per-token rates are scaled to per million tokens.
	require.NotNil(t, paid.Pricing.InputPerMtok)
	assert.Equal(t, 0.15, *paid.Pricing.InputPerMtok)
	require.NotNil(t, paid.Pricing.OutputPerMtok)
	assert.Equal(t, 0.6, *paid.Pricing.OutputPerMtok)

	free := byID["deepseek/deepseek-r1:free"].Metadata
	require.NotNil(t, free)
	require.NotNil(t, free.Pricing)
	require.NotNil(t, free.Pricing.InputPerMtok)
	assert.Equal(t, float64(0), *free.Pricing.InputPerMtok)
	require.NotNil(t, free.Pricing.OutputPerMtok)
	assert.Equal(t, float64(0), *free.Pricing.OutputPerMtok)

	// "-1" means OpenRouter cannot state the rate up front; reporting it as a
	// negative price would make an auto-routed model look cheaper than free.
	assert.Nil(t, pricingOf(byID["openrouter/auto"]), "auto-router must carry no pricing")
	assert.Nil(t, pricingOf(byID["acme/unpriced"]), "unpriced model must carry no pricing")
}

// strconv.ParseFloat accepts "NaN" and "Inf", and scaling a huge per-token rate
// to per-Mtok can overflow. Either would corrupt every downstream price
// comparison and cost calculation, so such rates report no price at all.
func TestListModels_RejectsNonFinitePricing(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"data":[
		{"id":"acme/nan","architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"NaN","completion":"NaN"}},
		{"id":"acme/inf","architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"Inf","completion":"Inf"}},
		{"id":"acme/overflow","architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"1e308","completion":"1e308"}},
		{"id":"acme/partial","architecture":{"output_modalities":["text"]},
		 "pricing":{"prompt":"0.000001","completion":"NaN"}}
	]}`)
	provider := newTestProvider(server.URL, server.Client())

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	byID := modelsByID(resp)

	for _, id := range []string{"acme/nan", "acme/inf", "acme/overflow"} {
		assert.Nil(t, pricingOf(byID[id]), "%s must carry no pricing", id)
	}

	// One unusable rate must not discard the other, usable one.
	partial := byID["acme/partial"].Metadata
	require.NotNil(t, partial)
	require.NotNil(t, partial.Pricing)
	require.NotNil(t, partial.Pricing.InputPerMtok)
	assert.Equal(t, float64(1), *partial.Pricing.InputPerMtok)
	assert.Nil(t, partial.Pricing.OutputPerMtok)
}
