package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/googlecommon"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/oauth2"
)

func TestNew(t *testing.T) {
	apiKey := "test-api-key"
	// Use NewWithHTTPClient to get concrete type for internal testing
	provider := NewWithHTTPClient(apiKey, nil, llmclient.Hooks{})
	got := provider.keys.Primary()
	assert.Equal(t, apiKey, got)
	assert.Equal(t, defaultModelsBaseURL, provider.modelsURL)
	assert.NotNil(t, provider.client)
}

func TestPrepareCachedContentCreatesAndReusesObject(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"name":"cachedContents/session-prefix","expireTime":"2099-01-01T00:00:00Z"}`)

	p := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	req := &core.ChatRequest{Model: "gemini-2.5-pro", PromptCachePlan: &core.PromptCachePlan{Key: "prefix-key"}}
	newBody := func() *geminiGenerateContentRequest {
		return &geminiGenerateContentRequest{
			SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: "system"}}},
			Contents: []geminiContent{
				{Role: "user", Parts: []geminiPart{{Text: "stable"}}},
				{Role: "user", Parts: []geminiPart{{Text: "dynamic"}}},
			},
			Tools: []geminiTool{{FunctionDeclarations: []geminiFunctionDeclaration{{Name: "lookup"}}}},
		}
	}
	first := newBody()
	p.prepareCachedContent(context.Background(), req, first)
	second := newBody()
	p.prepareCachedContent(context.Background(), req, second)
	require.Equal(t, 1, capture.Count())
	require.Equal(t, "/cachedContents", capture.Last(t).Path)

	for i, body := range []*geminiGenerateContentRequest{first, second} {
		require.Equal(t, "cachedContents/session-prefix", body.CachedContent)
		require.Len(t, body.Contents, 1)
		require.Nil(t, body.SystemInstruction, "body %d did not use cached prefix: %+v", i, body)
	}
}

func TestPrepareCachedContentSupportsSystemOnlyPrefix(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"name":"cachedContents/system-prefix","expireTime":"2099-01-01T00:00:00Z"}`)
	p := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	req := &core.ChatRequest{Model: "gemini-2.5-pro", PromptCachePlan: &core.PromptCachePlan{Key: "system"}}
	body := &geminiGenerateContentRequest{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: "stable system"}}},
		Contents:          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: "live turn"}}}},
	}
	p.prepareCachedContent(context.Background(), req, body)
	require.Equal(t, 1, capture.Count())
	require.Equal(t, "cachedContents/system-prefix", body.CachedContent)
	require.Len(t, body.Contents, 1)
	require.Nil(t, body.SystemInstruction, "system prefix was not cached: %+v", body)
}

func TestPrepareCachedContentFailureIsBestEffortAndBackedOff(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusBadRequest, `{"error":{"message":"unsupported"}}`)
	p := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	req := &core.ChatRequest{Model: "gemini-2.5-pro", PromptCachePlan: &core.PromptCachePlan{Key: "failure"}}
	newBody := func() *geminiGenerateContentRequest {
		return &geminiGenerateContentRequest{
			SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: "system"}}},
			Contents:          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: "live"}}}},
			Tools:             []geminiTool{{FunctionDeclarations: []geminiFunctionDeclaration{{Name: "lookup"}}}},
		}
	}
	first, second := newBody(), newBody()
	firstBefore, _ := json.Marshal(first)
	secondBefore, _ := json.Marshal(second)
	p.prepareCachedContent(context.Background(), req, first)
	p.prepareCachedContent(context.Background(), req, second)
	require.Equal(t, 1, capture.Count(), "second call must be backed off")

	firstAfter, _ := json.Marshal(first)
	secondAfter, _ := json.Marshal(second)
	require.Equal(t, firstAfter, firstBefore)
	require.Equal(t, secondAfter, secondBefore)
}

func TestPrepareCachedContentEmptyNameAndExpiringEntry(t *testing.T) {
	var creates atomic.Int32
	server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if creates.Add(1) == 1 {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_, _ = io.WriteString(w, `{"name":"cachedContents/recreated","expireTime":"2099-01-01T00:00:00Z"}`)
	})
	p := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	req := &core.ChatRequest{Model: "gemini-2.5-pro", PromptCachePlan: &core.PromptCachePlan{Key: "expiry"}}
	newBody := func() *geminiGenerateContentRequest {
		return &geminiGenerateContentRequest{SystemInstruction: &geminiContent{}, Contents: []geminiContent{{Role: "user"}}}
	}
	failed := newBody()
	before, _ := json.Marshal(failed)
	p.prepareCachedContent(context.Background(), req, failed)
	after, _ := json.Marshal(failed)
	require.Equal(t, after, before)

	scopedKey, ok := p.scopedCachedContentKey(context.Background(), req.PromptCachePlan.Key)
	require.True(t, ok)

	p.cacheMu.Lock()
	p.cacheObjects[scopedKey] = geminiCacheObject{name: "cachedContents/expiring", expiresAt: time.Now().Add(5 * time.Second)}
	p.cacheMu.Unlock()
	recreated := newBody()
	p.prepareCachedContent(context.Background(), req, recreated)
	require.Equal(t, int32(2), creates.Load())
	require.Equal(t, "cachedContents/recreated", recreated.CachedContent, "expiring entry was not recreated: %+v", recreated)
}

func TestPrepareCachedContentCoalescesConcurrentCreation(t *testing.T) {
	const callers = 12
	var begun atomic.Int32
	allBegun := make(chan struct{})
	handlerEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		handlerEntered <- struct{}{}
		<-allBegun
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"cachedContents/concurrent","expireTime":"2099-01-01T00:00:00Z"}`)
	})
	p := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	req := &core.ChatRequest{Model: "gemini-2.5-pro", PromptCachePlan: &core.PromptCachePlan{Key: "concurrent"}}
	bodies := make([]*geminiGenerateContentRequest, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			bodies[i] = &geminiGenerateContentRequest{SystemInstruction: &geminiContent{}, Contents: []geminiContent{{Role: "user"}}}
			if begun.Add(1) == callers {
				close(allBegun)
			}
			p.prepareCachedContent(context.Background(), req, bodies[i])
		})
	}
	<-handlerEntered
	<-allBegun
	close(release)
	wg.Wait()
	require.Equal(t, 1, capture.Count())

	for i, body := range bodies {
		require.Equal(t, "cachedContents/concurrent", body.CachedContent, "caller %d did not receive shared cache object: %+v", i, body)
	}
}

func TestPrepareCachedContentRequiresStableCredentialAndAIStudio(t *testing.T) {
	server, capture := providertest.Server(t, nil)
	p := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL)
	p.keys = providers.NewKeyring("one", "two")
	req := &core.ChatRequest{PromptCachePlan: &core.PromptCachePlan{Key: "prefix"}}
	body := &geminiGenerateContentRequest{SystemInstruction: &geminiContent{}, Contents: []geminiContent{{Role: "user"}}}
	_, ok := p.scopedCachedContentKey(context.Background(), "prefix")
	require.False(t, ok)

	ctx := core.WithSessionID(context.Background(), "session-a")
	first, ok := p.scopedCachedContentKey(ctx, "prefix")
	second, ok2 := p.scopedCachedContentKey(ctx, "prefix")
	require.True(t, ok)
	require.True(t, ok2)
	require.NotEmpty(t, first)
	require.Equal(t, second, first)

	p.backend = geminiBackendVertex
	p.prepareCachedContent(ctx, req, body)
	require.Empty(t, body.CachedContent)
	require.Zero(t, capture.Count())
}

func TestGeminiCacheObjectMapIsBoundedAndSweepsExpiredEntries(t *testing.T) {
	p := &Provider{}
	now := time.Now()
	p.cacheObjects = map[string]geminiCacheObject{
		"expired": {name: "old", expiresAt: now.Add(-time.Minute)},
	}
	for i := range geminiCacheObjectLimit + 20 {
		p.storeCachedContentObject(fmt.Sprintf("key-%d", i), geminiCacheObject{name: "cache", expiresAt: now.Add(time.Hour)}, now)
	}
	_, exists := p.cacheObjects["expired"]
	require.False(t, exists)
	require.LessOrEqual(t, len(p.cacheObjects), geminiCacheObjectLimit)
}

func TestNew_AIStudioRejectsGCPAuthAliases(t *testing.T) {
	for _, authType := range []string{"adc", "service_account"} {
		t.Run(authType, func(t *testing.T) {
			provider := New(providers.ProviderConfig{
				BaseURL:  defaultOpenAICompatibleBaseURL,
				AuthType: authType,
			}, providers.ProviderOptions{})

			geminiProvider, ok := provider.(*Provider)
			require.True(t, ok, "provider type = %T, want *Provider", provider)

			err := geminiProvider.ready()
			require.Error(t, err)
			require.Contains(t, err.Error(), "ai studio backend does not support GCP auth")
		})
	}
}

func TestNew_VertexAcceptsGCPAuthAliases(t *testing.T) {
	for _, authType := range []string{"adc", "service_account"} {
		t.Run(authType, func(t *testing.T) {
			p := NewVertexWithHTTPClient(providers.ProviderConfig{
				AuthType:       authType,
				VertexProject:  "prod-ai",
				VertexLocation: "us-central1",
			}, providers.ProviderOptions{}, http.DefaultClient)
			err := p.ready()
			require.NoError(t, err)
		})
	}
}

func TestNew_VertexConfigErrorUsesVertexProviderName(t *testing.T) {
	p := NewVertexWithHTTPClient(providers.ProviderConfig{
		AuthType: "api_key",
		BaseURL:  "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
	}, providers.ProviderOptions{}, http.DefaultClient)

	err := p.ready()
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, "vertex", gatewayErr.Provider)
}

func TestGeminiBaseURLs(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		wantCompat string
		wantNative string
	}{
		{
			name:       "empty uses official defaults",
			wantCompat: defaultOpenAICompatibleBaseURL,
			wantNative: defaultModelsBaseURL,
		},
		{
			name:       "official OpenAI-compatible default derives native default",
			configured: defaultOpenAICompatibleBaseURL,
			wantCompat: defaultOpenAICompatibleBaseURL,
			wantNative: defaultModelsBaseURL,
		},
		{
			name:       "official native default keeps OpenAI-compatible default",
			configured: defaultModelsBaseURL,
			wantCompat: defaultOpenAICompatibleBaseURL,
			wantNative: defaultModelsBaseURL,
		},
		{
			name:       "custom OpenAI-compatible URL derives native sibling",
			configured: "https://proxy.example.com/v1beta/openai/",
			wantCompat: "https://proxy.example.com/v1beta/openai",
			wantNative: "https://proxy.example.com/v1beta",
		},
		{
			name:       "custom URL without OpenAI suffix is used for both clients",
			configured: "https://proxy.example.com/gemini",
			wantCompat: "https://proxy.example.com/gemini",
			wantNative: "https://proxy.example.com/gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompat, gotNative := geminiBaseURLs(providers.ProviderConfig{BaseURL: tt.configured}, geminiBackendAIStudio)
			require.Equal(t, tt.wantCompat, gotCompat)
			require.Equal(t, tt.wantNative, gotNative)
		})
	}
}

func TestVertexBaseURLs(t *testing.T) {
	tests := []struct {
		name       string
		cfg        providers.ProviderConfig
		wantCompat string
		wantNative string
	}{
		{
			name: "derives official vertex bases from project and location",
			cfg: providers.ProviderConfig{
				VertexProject:  "prod-ai",
				VertexLocation: "us-central1",
			},
			wantCompat: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
		{
			name: "custom OpenAI-compatible vertex URL derives native sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
		{
			name: "custom native vertex URL derives OpenAI-compatible sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompat, gotNative := geminiBaseURLs(tt.cfg, geminiBackendVertex)
			require.Equal(t, tt.wantCompat, gotCompat)
			require.Equal(t, tt.wantNative, gotNative)
		})
	}
}

func TestVertexModelsBaseURL(t *testing.T) {
	tests := []struct {
		name       string
		nativeBase string
		want       string
	}{
		{
			name:       "official vertex native base",
			nativeBase: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
			want:       "https://aiplatform.googleapis.com/v1beta1/publishers/google",
		},
		{
			name:       "custom proxy vertex native base",
			nativeBase: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
			want:       "https://proxy.example.com/v1beta1/publishers/google",
		},
		{
			name:       "unknown custom base keeps native base",
			nativeBase: "https://proxy.example.com/custom/gemini",
			want:       "https://proxy.example.com/custom/gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := geminiModelsBaseURL(geminiBackendVertex, tt.nativeBase)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNewVertexWithHTTPClientAcceptsBaseURLWithoutProjectLocation(t *testing.T) {
	p := NewVertexWithHTTPClient(providers.ProviderConfig{
		BaseURL:  "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		AuthType: "gcp_adc",
	}, providers.ProviderOptions{}, http.DefaultClient)
	err := p.ready()
	require.NoError(t, err)
}

func TestVertexModelNormalization(t *testing.T) {
	tests := []struct {
		in         string
		wantNative string
		wantOpenAI string
	}{
		{in: "gemini-2.5-flash", wantNative: "gemini-2.5-flash", wantOpenAI: "google/gemini-2.5-flash"},
		{in: "models/gemini-2.5-flash", wantNative: "gemini-2.5-flash", wantOpenAI: "google/gemini-2.5-flash"},
		{in: "google/gemini-2.5-flash", wantNative: "gemini-2.5-flash", wantOpenAI: "google/gemini-2.5-flash"},
		{
			in:         "projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash",
			wantNative: "gemini-2.5-flash",
			wantOpenAI: "google/gemini-2.5-flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := normalizeGeminiModelID(tt.in)
			require.Equal(t, tt.wantNative, got)
			got = vertexOpenAIModelID(tt.in)
			require.Equal(t, tt.wantOpenAI, got)
		})
	}
}

func TestNew_CustomBaseURLDerivesNativeBaseURL(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	provider := New(providers.ProviderConfig{
		APIKey:  "test-api-key",
		BaseURL: "https://proxy.example.com/v1beta/openai",
	}, providers.ProviderOptions{})

	geminiProvider, ok := provider.(*Provider)
	require.True(t, ok, "provider type = %T, want *Provider", provider)
	require.True(t, geminiProvider.useNativeAPI)
	require.Equal(t, "https://proxy.example.com/v1beta/openai", geminiProvider.client.BaseURL())
	require.Equal(t, "https://proxy.example.com/v1beta", geminiProvider.modelsURL)
	require.Equal(t, "https://proxy.example.com/v1beta", geminiProvider.nativeClient.BaseURL())
}

func TestSetBaseURLDerivesNativeRouting(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "gemini-native-baseurl",
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "ok"}]},
			"finishReason": "STOP"
		}]
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1beta/openai")

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "gemini-native-baseurl", resp.ID)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/models/gemini-2.5-flash:generateContent", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))
	assert.Empty(t, sent.Header.Get("Authorization"))
}

func TestSetBaseURLDerivesModelsURL(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"models": [{
			"name": "models/gemini-2.5-flash",
			"displayName": "Gemini 2.5 Flash",
			"supportedGenerationMethods": ["generateContent", "streamGenerateContent"],
			"inputTokenLimit": 1048576,
			"outputTokenLimit": 8192
		}]
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1beta/openai")

	require.Equal(t, server.URL+"/v1beta/openai", provider.client.BaseURL())
	require.Equal(t, server.URL+"/v1beta", provider.modelsURL)
	require.Equal(t, server.URL+"/v1beta", provider.nativeClient.BaseURL())

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "gemini-2.5-flash", resp.Data[0].ID)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/models", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))
}

// ListModels must stamp modes/categories from supportedGenerationMethods so
// embedding models are classified even when the remote model registry has no
// entry for them (new or preview IDs).
func TestListModels_StampsDiscoveredModes(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"models": [{
			"name": "models/gemini-2.5-flash",
			"supportedGenerationMethods": ["generateContent", "streamGenerateContent"]
		}, {
			"name": "models/text-embedding-004",
			"supportedGenerationMethods": ["embedContent"]
		}]
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1beta/openai")

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	byID := make(map[string]core.Model, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	chat, ok := byID["gemini-2.5-flash"]
	require.True(t, ok)
	require.NotNil(t, chat.Metadata, "gemini-2.5-flash missing or has no metadata: %+v", resp.Data)
	assert.Len(t, chat.Metadata.Modes, 1)
	assert.Equal(t, "chat", chat.Metadata.Modes[0])
	assert.Len(t, chat.Metadata.Categories, 1)
	assert.Equal(t, core.CategoryTextGeneration, chat.Metadata.Categories[0])

	embed, ok := byID["text-embedding-004"]
	require.True(t, ok)
	require.NotNil(t, embed.Metadata, "text-embedding-004 missing or has no metadata: %+v", resp.Data)
	assert.Len(t, embed.Metadata.Modes, 1)
	assert.Equal(t, "embedding", embed.Metadata.Modes[0])
	assert.Len(t, embed.Metadata.Categories, 1)
	assert.Equal(t, core.CategoryEmbedding, embed.Metadata.Categories[0])
}

func TestVertexNativeChatUsesOAuthAuthorization(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "vertex-native",
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "ok"}]},
			"finishReason": "STOP"
		}]
	}`)

	p := newVertexTestProvider(server, true)
	resp, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "vertex-native", resp.ID)
	assert.Equal(t, "vertex", resp.Provider)

	sent := capture.Last(t)
	assert.Equal(t, "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent", sent.Path)
	assert.Equal(t, "Bearer vertex-token", sent.Header.Get("Authorization"))
	assert.Empty(t, sent.Header.Get("x-goog-api-key"))
}

func TestVertexNativeBlockedPromptUsesVertexProviderName(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "vertex-blocked",
		"promptFeedback": {
			"blockReason": "SAFETY",
			"blockReasonMessage": "unsafe prompt"
		}
	}`)

	p := newVertexTestProvider(server, true)
	_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "blocked"},
		},
	})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, "vertex", gatewayErr.Provider)
	assert.Contains(t, gatewayErr.Message, "SAFETY: unsafe prompt")
	assert.Equal(t, "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent", capture.Last(t).Path)
}

func TestVertexNativeStreamUsesVertexProviderName(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.SSEServer(t, `data: {"responseId":"vertex-stream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}

`)

	p := newVertexTestProvider(server, true)
	body, err := p.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	chunks := parseOpenAIStreamChunks(t, string(raw))
	require.NotEmpty(t, chunks)

	for _, chunk := range chunks {
		assert.Equal(t, "vertex", chunk["provider"], "stream %q", raw)
	}

	sent := capture.Last(t)
	assert.Equal(t, "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent", sent.Path)
	assert.Equal(t, "Bearer vertex-token", sent.Header.Get("Authorization"))
}

func TestVertexNativeStreamResponsesUsesVertexProviderName(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.SSEServer(t, `data: {"responseId":"vertex-responses-stream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}

`)

	p := newVertexTestProvider(server, true)
	body, err := p.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "google/gemini-2.5-flash",
		Input: "Hello",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"provider":"vertex"`)
	assert.Equal(t, "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent", capture.Last(t).Path)
}

func TestVertexOpenAICompatibleChatUsesOAuthAndGoogleModelPrefix(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id": "vertex-openai",
		"object": "chat.completion",
		"created": 1677652288,
		"model": "google/gemini-2.5-flash",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "ok"},
			"finish_reason": "stop"
		}]
	}`)

	p := newVertexTestProvider(server, false)
	resp, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "vertex-openai", resp.ID)
	assert.Equal(t, "vertex", resp.Provider)

	sent := capture.Last(t)
	assert.Equal(t, "/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/chat/completions", sent.Path)
	assert.Equal(t, "Bearer vertex-token", sent.Header.Get("Authorization"))
	assert.Empty(t, sent.Header.Get("x-goog-api-key"))
	assert.Equal(t, "google/gemini-2.5-flash", sent.JSON(t)["model"])
}

func TestVertexOpenAICompatibleEmbeddingsUsesOAuthAndGoogleModelPrefix(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"object": "list",
		"model": "google/text-embedding-005",
		"data": [{
			"object": "embedding",
			"embedding": [0.1, 0.2],
			"index": 0
		}],
		"usage": {"prompt_tokens": 1, "total_tokens": 1}
	}`)

	p := newVertexTestProvider(server, false)
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-005",
		Input: "text",
	})
	require.NoError(t, err)
	assert.Equal(t, "google/text-embedding-005", resp.Model)
	assert.Equal(t, "vertex", resp.Provider)
	require.Len(t, resp.Data, 1)

	var embedding []float64
	require.NoError(t, json.Unmarshal(resp.Data[0].Embedding, &embedding), "embedding = %s, want float array", resp.Data[0].Embedding)
	assert.Equal(t, []float64{0.1, 0.2}, embedding)

	sent := capture.Last(t)
	assert.Equal(t, "/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/embeddings", sent.Path)
	assert.Equal(t, "Bearer vertex-token", sent.Header.Get("Authorization"))
	assert.Empty(t, sent.Header.Get("x-goog-api-key"))
	payload := sent.JSON(t)
	assert.Equal(t, "google/text-embedding-005", payload["model"])
	assert.Equal(t, "text", payload["input"])
}

func TestVertexListModelsAcceptsPublisherModelsResponse(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"publisherModels": [
			{"name": "publishers/google/models/gemini-2.5-flash"},
			{"name": "publishers/google/models/text-embedding-005"},
			{"name": "publishers/google/models/imagen-4.0"}
		]
	}`)

	p := newVertexTestProvider(server, true)
	resp, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 3)
	assert.Equal(t, "google/gemini-2.5-flash", resp.Data[0].ID)
	assert.Equal(t, "google/text-embedding-005", resp.Data[1].ID)
	assert.Equal(t, "google/imagen-4.0", resp.Data[2].ID)
	assert.Equal(t, []string{"image_generation"}, resp.Data[2].Metadata.Modes)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta1/publishers/google/models", sent.Path)
	assert.Equal(t, "Bearer vertex-token", sent.Header.Get("Authorization"))
}

func TestVertexListModelsErrorsUseVertexProviderName(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "native parse error",
			body: `{"publisherModels":"bad"}`,
		},
		{
			name: "unexpected format",
			body: `{"unexpected":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, http.StatusOK, tt.body)

			p := newVertexTestProvider(server, true)
			_, err := p.ListModels(context.Background())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			require.Equal(t, "vertex", gatewayErr.Provider)
		})
	}
}

func newVertexTestProvider(server *httptest.Server, native bool) *Provider {
	tokenClient := googlecommon.HTTPClient(server.Client(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "vertex-token",
		TokenType:   "Bearer",
	}), "")
	p := &Provider{
		backend:      geminiBackendVertex,
		authType:     geminiAuthTypeGCPADC,
		useNativeAPI: native,
	}
	openAIBaseURL := server.URL + "/v1/projects/prod-ai/locations/us-central1/endpoints/openapi"
	nativeBaseURL := server.URL + "/v1/projects/prod-ai/locations/us-central1/publishers/google"
	modelsBaseURL := server.URL + "/v1beta1/publishers/google"
	openAICfg := llmclient.DefaultConfig("vertex", openAIBaseURL)
	nativeCfg := llmclient.DefaultConfig("vertex", nativeBaseURL)
	modelsCfg := llmclient.DefaultConfig("vertex", modelsBaseURL)
	p.client = llmclient.NewWithHTTPClient(tokenClient, openAICfg, p.setHeaders)
	p.nativeClient = llmclient.NewWithHTTPClient(tokenClient, nativeCfg, p.setNativeHeaders)
	p.modelsClient = llmclient.NewWithHTTPClient(tokenClient, modelsCfg, p.setNativeHeaders)
	p.modelsURL = modelsBaseURL
	return p
}

func TestChatCompletion(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ChatResponse)
	}{
		{
			name:       "successful request",
			statusCode: http.StatusOK,
			responseBody: `{
				"id": "gemini-123",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gemini-2.0-flash",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello! How can I help you today?"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 20,
					"total_tokens": 30
				}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				assert.Equal(t, "gemini-123", resp.ID)
				assert.Equal(t, "gemini-2.0-flash", resp.Model)
				assert.Equal(t, "gemini", resp.Provider)
				require.Len(t, resp.Choices, 1)
				assert.Equal(t, "Hello! How can I help you today?", resp.Choices[0].Message.Content)
				assert.Equal(t, 10, resp.Usage.PromptTokens)
				assert.Equal(t, 20, resp.Usage.CompletionTokens)
				assert.Equal(t, 30, resp.Usage.TotalTokens)
			},
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"error": {"message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error": {"message": "Internal server error"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "gemini-2.0-flash",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			resp, err := provider.ChatCompletion(context.Background(), req)

			sent := capture.Last(t)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer test-api-key", sent.Header.Get("Authorization"))
			assert.Equal(t, "gemini-2.0-flash", sent.JSON(t)["model"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestChatCompletion_UsesNativeGenerateContentByDefault(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "gemini-native-123",
		"candidates": [{
			"index": 0,
			"content": {"role": "model", "parts": [{"text": "Hello from native Gemini"}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 7,
			"candidatesTokenCount": 5,
			"totalTokenCount": 12
		}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	maxTokens := 128
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "gemini-2.5-flash",
		MaxTokens: &maxTokens,
		Messages: []core.Message{
			{Role: "system", Content: "Be concise."},
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "gemini-native-123", resp.ID)
	assert.Equal(t, "gemini", resp.Provider)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello from native Gemini", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 12, resp.Usage.TotalTokens)

	sent := capture.Last(t)
	assert.Equal(t, http.MethodPost, sent.Method)
	assert.Equal(t, "/models/gemini-2.5-flash:generateContent", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))
	assert.Empty(t, sent.Header.Get("Authorization"))

	payload := sent.JSON(t)
	assert.NotContains(t, payload, "messages")
	assert.Contains(t, payload, "contents")
	assert.Contains(t, payload, "system_instruction")
	generationConfig, ok := payload["generationConfig"].(map[string]any)
	require.True(t, ok, "generationConfig = %#v, want object", payload["generationConfig"])
	assert.Equal(t, float64(128), generationConfig["maxOutputTokens"])
}

func TestGeminiGenerationConfig_UsesTypedTopP(t *testing.T) {
	topP := 0.8
	cfg := geminiGenerationConfig(&core.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		TopP:     &topP,
	})
	require.Equal(t, 0.8, cfg["topP"])
}

func TestConvertResponsesRequestToGeminiPreservesTopP(t *testing.T) {
	topP := 0.7
	chatReq, err := providers.ConvertResponsesRequestToChat(&core.ResponsesRequest{
		Model: "gemini-2.5-flash",
		Input: "hi",
		TopP:  &topP,
	})
	require.NoError(t, err)

	geminiReq, err := convertChatRequestToGemini(chatReq)
	require.NoError(t, err)
	require.Equal(t, 0.7, geminiReq.GenerationConfig["topP"])
}

func TestChatCompletion_NativeUsageMetadata(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "gemini-native-usage",
		"candidates": [{
			"index": 0,
			"content": {"role": "model", "parts": [{"text": "Done"}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 100,
			"cachedContentTokenCount": 40,
			"candidatesTokenCount": 20,
			"thoughtsTokenCount": 7,
			"totalTokenCount": 127,
			"promptTokensDetails": [
				{"modality": "TEXT", "tokenCount": 60},
				{"modality": "AUDIO", "tokenCount": 40}
			],
			"candidatesTokensDetails": [
				{"modality": "AUDIO", "tokenCount": 5}
			]
		}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 100, resp.Usage.PromptTokens)
	assert.Equal(t, 27, resp.Usage.CompletionTokens)
	assert.Equal(t, 127, resp.Usage.TotalTokens)
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 40, resp.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 40, resp.Usage.PromptTokensDetails.AudioTokens)
	require.NotNil(t, resp.Usage.CompletionTokensDetails)
	assert.Equal(t, 7, resp.Usage.CompletionTokensDetails.ReasoningTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokensDetails.AudioTokens)
	assert.Equal(t, 40, resp.Usage.RawUsage["prompt_cached_tokens"])
	assert.Equal(t, 7, resp.Usage.RawUsage["completion_reasoning_tokens"])
}

func TestCopyJSONNumberAcceptsOnlyNumericValues(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantSet bool
		want    float64
	}{
		{name: "number", raw: `42`, wantSet: true, want: 42},
		{name: "numeric string", raw: `"42.5"`, wantSet: true, want: 42.5},
		{name: "object", raw: `{"value":42}`, wantSet: false},
		{name: "array", raw: `[42]`, wantSet: false},
		{name: "boolean", raw: `true`, wantSet: false},
		{name: "non numeric string", raw: `"fast"`, wantSet: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := map[string]any{}
			copyJSONNumber(json.RawMessage(tt.raw), cfg, "value")

			got, ok := cfg["value"]
			require.Equal(t, tt.wantSet, ok, "cfg = %#v", cfg)
			if !tt.wantSet {
				return
			}
			require.IsType(t, float64(0), got)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestChatCompletion_NativeBlockedPromptReturnsError(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "gemini-blocked",
		"promptFeedback": {
			"blockReason": "SAFETY",
			"blockReasonMessage": "unsafe prompt"
		}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "blocked"},
		},
	})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Message, "SAFETY: unsafe prompt")
}

func TestChatCompletion_NativeRejectsRemoteImageURL(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{{
			Role: "user",
			Content: []core.ContentPart{
				{Type: "text", Text: "Describe the image."},
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
			},
		}},
	})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Message, "supports only data: URLs")
}

func TestChatCompletion_NativeFunctionCallTranslation(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "gemini-tools-123",
		"candidates": [{
			"content": {"role": "model", "parts": [{
				"functionCall": {
					"id": "call_native",
					"name": "lookup_weather",
					"args": {"city": "Warsaw"}
				}
			}]},
			"finishReason": "STOP"
		}]
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Weather?"},
		},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "lookup_weather",
				"description": "Look up weather",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []any{"city"},
				},
			},
		}},
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "lookup_weather"},
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "tool_calls", resp.Choices[0].FinishReason)
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)

	call := resp.Choices[0].Message.ToolCalls[0]
	assert.Equal(t, "call_native", call.ID)
	assert.Equal(t, "lookup_weather", call.Function.Name)
	assert.Equal(t, `{"city":"Warsaw"}`, call.Function.Arguments)

	payload := capture.Last(t).JSON(t)
	tools, ok := payload["tools"].([]any)
	require.True(t, ok, "tools = %#v, want array", payload["tools"])
	assert.Len(t, tools, 1)
	toolConfig, ok := payload["toolConfig"].(map[string]any)
	require.True(t, ok, "toolConfig = %#v, want object", payload["toolConfig"])
	functionConfig, _ := toolConfig["functionCallingConfig"].(map[string]any)
	assert.Equal(t, "ANY", functionConfig["mode"])
}

func TestStreamChatCompletion(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
	}{
		{
			name:       "successful streaming request",
			statusCode: http.StatusOK,
			responseBody: `data: {"id":"gemini-123","object":"chat.completion.chunk","created":1677652288,"model":"gemini-2.0-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"gemini-123","object":"chat.completion.chunk","created":1677652288,"model":"gemini-2.0-flash","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`,
			expectedError: false,
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.responseBody)
			})

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "gemini-2.0-flash",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			body, err := provider.StreamChatCompletion(context.Background(), req)

			sent := capture.Last(t)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer test-api-key", sent.Header.Get("Authorization"))
			stream, _ := sent.JSON(t)["stream"].(bool)
			assert.True(t, stream)

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, body)
			defer func() { _ = body.Close() }()

			respBody, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.Equal(t, tt.responseBody, string(respBody))
		})
	}
}

func TestStreamChatCompletion_UsesNativeStreamByDefault(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.SSEServer(t, `data: {"responseId":"gemini-stream-123","candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":1,"totalTokenCount":5}}

data: {"responseId":"gemini-stream-123","candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}

`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	stream := string(raw)
	assert.Contains(t, stream, `"id":"gemini-stream-123"`)
	assert.Contains(t, stream, `"content":"Hello"`)
	assert.Contains(t, stream, `"content":"!"`)
	assert.Contains(t, stream, `"finish_reason":"stop"`)
	assert.Contains(t, stream, `"total_tokens":6`)
	assert.Contains(t, stream, "data: [DONE]")

	usageChunks := 0
	for _, chunk := range parseOpenAIStreamChunks(t, stream) {
		if _, ok := chunk["usage"]; !ok {
			continue
		}
		usageChunks++
		choices, ok := chunk["choices"].([]any)
		require.True(t, ok, "usage chunk choices = %#v, want array", chunk["choices"])
		assert.Empty(t, choices)
	}
	assert.Equal(t, 1, usageChunks, "stream %q", stream)

	sent := capture.Last(t)
	assert.Equal(t, "/models/gemini-2.5-flash:streamGenerateContent", sent.Path)
	assert.Equal(t, "sse", sent.Query.Get("alt"))
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))
	assert.NotContains(t, sent.JSON(t), "stream")
}

func TestParseOpenAIStreamChunksStopsAtDone(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		wantChunks int
		wantID     string
	}{
		{
			name: "after a data chunk",
			stream: `data: {"id":"before-done"}

data: [DONE]

data: {}
`,
			wantChunks: 1,
			wantID:     "before-done",
		},
		{
			name:       "before any data chunks",
			stream:     "data: [DONE]\n\ndata: {}\n\n",
			wantChunks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := parseOpenAIStreamChunks(t, tt.stream)
			require.Len(t, chunks, tt.wantChunks, "chunks = %#v", chunks)
			if tt.wantID != "" {
				assert.Equal(t, tt.wantID, chunks[0]["id"])
			}
		})
	}
}

func parseOpenAIStreamChunks(t *testing.T, stream string) []map[string]any {
	t.Helper()

	var chunks []map[string]any
	for line := range strings.SplitSeq(stream, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &chunk), "stream chunk %q", payload)
		chunks = append(chunks, chunk)
	}
	return chunks
}

func TestStreamChatCompletion_NativePerChoiceState(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"responseId":"gemini-stream-choice-state","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_0","name":"lookup_weather","args":{"city":"Warsaw"}}}]},"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"plain text"}]},"finishReason":"STOP"}]}

`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	stream := string(raw)
	assert.Equal(t, 2, strings.Count(stream, `"role":"assistant"`), "stream %q", stream)
	assert.Equal(t, 1, strings.Count(stream, `"finish_reason":"tool_calls"`), "stream %q", stream)
	assert.Equal(t, 1, strings.Count(stream, `"finish_reason":"stop"`), "stream %q", stream)
}

func TestStreamChatCompletion_NativeBlockedPromptEmitsError(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"responseId":"gemini-stream-blocked","promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"unsafe prompt"}}

`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "blocked"},
		},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	stream := string(raw)
	assert.Contains(t, stream, `"type":"provider_error"`)
	assert.Contains(t, stream, "Gemini blocked prompt: SAFETY: unsafe prompt")
	assert.Contains(t, stream, "data: [DONE]")
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ModelsResponse)
	}{
		{
			name:       "successful request",
			statusCode: http.StatusOK,
			responseBody: `{
				"models": [
					{
						"name": "models/gemini-2.0-flash",
						"displayName": "Gemini 2.0 Flash",
						"description": "Fast and efficient model",
						"supportedGenerationMethods": ["generateContent", "streamGenerateContent"],
						"inputTokenLimit": 32768,
						"outputTokenLimit": 8192
					},
					{
						"name": "models/gemini-1.5-pro",
						"displayName": "Gemini 1.5 Pro",
						"description": "Advanced reasoning and complex tasks",
						"supportedGenerationMethods": ["generateContent", "streamGenerateContent"],
						"inputTokenLimit": 1048576,
						"outputTokenLimit": 8192
					},
					{
						"name": "models/embedding-001",
						"displayName": "Text Embedding",
						"description": "Embedding model",
						"supportedGenerationMethods": ["embedContent"],
						"inputTokenLimit": 2048,
						"outputTokenLimit": 1
					}
				]
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				assert.Equal(t, "list", resp.Object)
				require.Len(t, resp.Data, 2)
				assert.Equal(t, "gemini-2.0-flash", resp.Data[0].ID)
				assert.Equal(t, "google", resp.Data[0].OwnedBy)
				assert.Equal(t, "gemini-1.5-pro", resp.Data[1].ID)
			},
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetModelsURL(server.URL)

			resp, err := provider.ListModels(context.Background())

			sent := capture.Last(t)
			assert.Equal(t, http.MethodGet, sent.Method)
			assert.Equal(t, "/models", sent.Path)
			assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestChatCompletionWithContext(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := &core.ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err := provider.ChatCompletion(ctx, req)
	assert.Error(t, err)
}

func TestResponses(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"id": "gemini-123",
		"object": "chat.completion",
		"created": 1677652288,
		"model": "gemini-2.0-flash",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello! How can I help you today?"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30
		}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "gemini-2.0-flash",
		Input: "Hello",
	}

	resp, err := provider.Responses(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "gemini-123", resp.ID)
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "gemini-2.0-flash", resp.Model)
}

func TestResponses_Native(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"responseId": "gemini-native-response",
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "Native response"}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 3,
			"totalTokenCount": 8
		}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	maxOutputTokens := 64
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:           "gemini-2.5-flash",
		Instructions:    "Be concise.",
		Input:           "Hello",
		MaxOutputTokens: &maxOutputTokens,
	})
	require.NoError(t, err)
	assert.Equal(t, "gemini-native-response", resp.ID)
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "gemini-2.5-flash", resp.Model)
	assert.Equal(t, "gemini", resp.Provider)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)
	assert.Equal(t, "Native response", resp.Output[0].Content[0].Text)

	sent := capture.Last(t)
	assert.Equal(t, http.MethodPost, sent.Method)
	assert.Equal(t, "/models/gemini-2.5-flash:generateContent", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))
	assert.Empty(t, sent.Header.Get("Authorization"))

	payload := sent.JSON(t)
	assert.NotContains(t, payload, "messages")
	assertSystemInstruction(t, payload, "Be concise.")
	assertSingleUserText(t, payload, "Hello")
	generationConfig, ok := payload["generationConfig"].(map[string]any)
	require.True(t, ok, "generationConfig = %#v, want object", payload["generationConfig"])
	assert.Equal(t, float64(64), generationConfig["maxOutputTokens"])
}

// assertSystemInstruction checks the native payload carries a single
// system_instruction text part.
func assertSystemInstruction(t *testing.T, payload map[string]any, want string) {
	t.Helper()
	systemInstruction, ok := payload["system_instruction"].(map[string]any)
	require.True(t, ok, "system_instruction = %#v, want object", payload["system_instruction"])
	systemParts, ok := systemInstruction["parts"].([]any)
	require.True(t, ok, "system_instruction.parts = %#v, want array", systemInstruction["parts"])
	require.Len(t, systemParts, 1)
	part, _ := systemParts[0].(map[string]any)
	assert.Equal(t, want, part["text"])
}

// assertSingleUserText checks the native payload has exactly one user
// content with one text part.
func assertSingleUserText(t *testing.T, payload map[string]any, want string) {
	t.Helper()
	contents, ok := payload["contents"].([]any)
	require.True(t, ok, "contents = %#v, want array", payload["contents"])
	require.Len(t, contents, 1)
	content, _ := contents[0].(map[string]any)
	assert.Equal(t, "user", content["role"])
	parts, _ := content["parts"].([]any)
	require.Len(t, parts, 1)
	part, _ := parts[0].(map[string]any)
	assert.Equal(t, want, part["text"])
}

func TestStreamResponses(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server, _ := providertest.SSEServer(t, `data: {"id":"gemini-123","object":"chat.completion.chunk","created":1677652288,"model":"gemini-2.0-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: [DONE]
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "gemini-2.0-flash",
		Input: "Hello",
	}

	body, err := provider.StreamResponses(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, body)

	defer func() { _ = body.Close() }()

	respBody, err := io.ReadAll(body)
	require.NoError(t, err)

	responseStr := string(respBody)
	assert.Contains(t, responseStr, "response.created")
	assert.Contains(t, responseStr, "[DONE]")
}

func TestStreamResponses_Native(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, capture := providertest.SSEServer(t, `data: {"responseId":"gemini-native-stream-response","candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}

data: {"responseId":"gemini-native-stream-response","candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}]}

`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:        "gemini-2.5-flash",
		Instructions: "Be concise.",
		Input:        "Hello",
	})
	require.NoError(t, err)
	require.NotNil(t, body)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	stream := string(raw)
	assert.Contains(t, stream, "response.created")
	assert.Contains(t, stream, "response.output_text.delta")
	assert.Contains(t, stream, `"delta":"Hello"`)
	assert.Contains(t, stream, `"delta":"!"`)
	assert.Contains(t, stream, "data: [DONE]")

	sent := capture.Last(t)
	assert.Equal(t, http.MethodPost, sent.Method)
	assert.Equal(t, "/models/gemini-2.5-flash:streamGenerateContent", sent.Path)
	assert.Equal(t, "sse", sent.Query.Get("alt"))
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))
	assert.Empty(t, sent.Header.Get("Authorization"))

	payload := sent.JSON(t)
	assert.NotContains(t, payload, "stream")
	assertSystemInstruction(t, payload, "Be concise.")
	assertSingleUserText(t, payload, "Hello")
}

func TestGeminiModelSupportedMethods_EmptyMethodFallback(t *testing.T) {
	tests := []struct {
		model                          string
		wantChat, wantEmbed, wantImage bool
	}{
		{model: "gemini-2.5-flash", wantChat: true},
		{model: "gemini-embedding-001", wantEmbed: true},
		{model: "text-embedding-004", wantEmbed: true},
		{model: "imagen-4.0-generate-001", wantImage: true},
	}
	for _, tt := range tests {
		gotChat, gotEmbed, gotImage := geminiModelSupportedMethods(tt.model, nil)
		assert.Equal(t, tt.wantChat, gotChat, "%s chat", tt.model)
		assert.Equal(t, tt.wantEmbed, gotEmbed, "%s embed", tt.model)
		assert.Equal(t, tt.wantImage, gotImage, "%s image", tt.model)
	}
}
