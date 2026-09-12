package llamacpp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonRoute answers with status and body as JSON.
func jsonRoute(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// countPath returns how many recorded requests hit path.
func countPath(capture *providertest.Capture, path string) int {
	n := 0
	for _, req := range capture.All() {
		if req.Path == path {
			n++
		}
	}
	return n
}

// legacyListing is the /v1/models payload of builds whose meta object predates
// n_ctx, leaving only the GGUF's trained context.
const legacyListing = `{
	"object":"list",
	"data":[{
		"id":"Meta-Llama-3.1-8B-Instruct",
		"object":"model",
		"created":1735142223,
		"owned_by":"llamacpp",
		"meta":{"vocab_type":2,"n_vocab":128256,"n_ctx_train":131072,"n_embd":4096,"n_params":8030261312,"size":4912898304}
	}]
}`

func TestListModels_SurfacesServerReportedMetadata(t *testing.T) {
	// realListing is a verbatim llama-server b10470 payload (gemma-3-270m-it
	// started with -c 2048), whose meta carries the running context as n_ctx.
	const realListing = `{
		"object":"list",
		"data":[{
			"id":"gemma-3-270m-it",
			"object":"model",
			"created":1787218858,
			"owned_by":"llamacpp",
			"meta":{"vocab_type":1,"n_vocab":262144,"n_ctx":2048,"n_ctx_train":32768,"n_embd":640,"n_params":268098176,"size":285018624,"ftype":"Q8_0"}
		}]
	}`

	tests := []struct {
		name              string
		listing           string
		propsStatus       int
		props             string
		wantContextWindow int
		wantModelID       string
		wantCapabilities  map[string]bool
		wantPropsFetched  bool
	}{
		{
			// meta.n_ctx is per-model, so it must win even over a /props that
			// disagrees — in router mode /props cannot be attributed at all.
			name:              "listing n_ctx wins over props and trained context",
			listing:           realListing,
			propsStatus:       http.StatusOK,
			props:             `{"default_generation_settings":{"n_ctx":9999},"modalities":{"vision":false,"video":false,"audio":false}}`,
			wantContextWindow: 2048,
			wantModelID:       "gemma-3-270m-it",
			wantPropsFetched:  true,
		},
		{
			name:              "props runtime context wins over trained context on older builds",
			listing:           legacyListing,
			propsStatus:       http.StatusOK,
			props:             `{"default_generation_settings":{"n_ctx":8192},"total_slots":1,"modalities":{"vision":false}}`,
			wantContextWindow: 8192,
			wantPropsFetched:  true,
		},
		{
			name:              "trained context is the fallback when props is unavailable",
			listing:           legacyListing,
			propsStatus:       http.StatusNotFound,
			props:             `{"error":"not found"}`,
			wantContextWindow: 131072,
			wantPropsFetched:  true,
		},
		{
			// LM Studio answers /props with 200 and an error body rather than a
			// 404, so a decodable-but-empty payload must not be mistaken for a
			// server reporting a zero context.
			name:              "props answered with an unrelated 200 payload",
			listing:           legacyListing,
			propsStatus:       http.StatusOK,
			props:             `{"error":"Unexpected endpoint or method. (GET /props)"}`,
			wantContextWindow: 131072,
			wantPropsFetched:  true,
		},
		{
			// A truncated or non-JSON body must not be read as a zero context.
			name:              "props answered with malformed json",
			listing:           legacyListing,
			propsStatus:       http.StatusOK,
			props:             `{"default_generation_settings":{"n_ctx":`,
			wantContextWindow: 131072,
			wantPropsFetched:  true,
		},
		{
			name:        "supported modalities become capabilities",
			listing:     legacyListing,
			propsStatus: http.StatusOK,
			// "telepathy" stands in for a modality a future llama.cpp adds: it
			// must not become a public capability on its own.
			props:             `{"default_generation_settings":{"n_ctx":4096},"modalities":{"vision":true,"video":true,"audio":false,"telepathy":true}}`,
			wantContextWindow: 4096,
			wantCapabilities:  map[string]bool{"vision": true, "video": true},
			wantPropsFetched:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
				"/v1/models": jsonRoute(http.StatusOK, tt.listing),
				"/props":     jsonRoute(tt.propsStatus, tt.props),
			})

			provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

			resp, err := provider.ListModels(context.Background())
			require.NoError(t, err)
			require.Len(t, resp.Data, 1)
			assert.Equal(t, tt.wantPropsFetched, countPath(capture, "/props") == 1)

			model := resp.Data[0]
			wantID := tt.wantModelID
			if wantID == "" {
				wantID = "Meta-Llama-3.1-8B-Instruct"
			}
			assert.Equal(t, wantID, model.ID)
			require.NotNil(t, model.Metadata)
			require.NotNil(t, model.Metadata.ContextWindow)
			assert.Equal(t, tt.wantContextWindow, *model.Metadata.ContextWindow)
			assert.Len(t, model.Metadata.Capabilities, len(tt.wantCapabilities))
			for name, want := range tt.wantCapabilities {
				assert.Equal(t, want, model.Metadata.Capabilities[name], "capability %q", name)
			}
			// Modes must stay empty so the registry's ID heuristic can still
			// classify local embedding and reranking GGUFs.
			assert.Empty(t, model.Metadata.Modes)
		})
	}
}

func TestListModels_RouterModeKeepsPerModelContextAndSkipsProps(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/v1/models": jsonRoute(http.StatusOK, `{
			"object":"list",
			"data":[
				{"id":"gemma-3-4b","object":"model","meta":{"n_ctx":8192,"n_ctx_train":131072}},
				{"id":"qwen3-8b","object":"model","meta":{"n_ctx":32768,"n_ctx_train":262144}}
			]
		}`),
		"/props": jsonRoute(http.StatusOK, `{"default_generation_settings":{"n_ctx":512}}`),
	})

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Zero(t, countPath(capture, "/props"), "router mode must not probe /props")

	want := map[string]int{"gemma-3-4b": 8192, "qwen3-8b": 32768}
	for _, model := range resp.Data {
		require.NotNil(t, model.Metadata)
		require.NotNil(t, model.Metadata.ContextWindow, "model %q lost its context window", model.ID)
		assert.Equal(t, want[model.ID], *model.Metadata.ContextWindow, "model %q context window", model.ID)
	}
}

func TestListModels_LeavesMetadataUnsetWhenServerReportsNothing(t *testing.T) {
	server, _ := providertest.RouteServer(t, map[string]http.HandlerFunc{
		// LM Studio and other plain OpenAI-compatible servers omit "meta".
		"/v1/models": jsonRoute(http.StatusOK, `{"data":[{"id":"local-model"}]}`),
		"/props":     jsonRoute(http.StatusOK, `{"default_generation_settings":{"n_ctx":0}}`),
	})

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, "model", resp.Data[0].Object)
	assert.Nil(t, resp.Data[0].Metadata)
}

// TestListModels_FailingPropsLeavesNativeRoutesUsable pins the isolation of the
// optional /props call: it must not retry against the shared native-route
// budget, nor trip the circuit breaker those routes depend on. Sharing
// rootClient here cost four attempts per listing and locked /health out
// entirely after six discovery cycles.
func TestListModels_FailingPropsLeavesNativeRoutesUsable(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/v1/models": jsonRoute(http.StatusOK, `{"object":"list","data":[{"id":"m","object":"model","meta":{"n_ctx_train":8192}}]}`),
		"/props":     jsonRoute(http.StatusServiceUnavailable, `{"error":"unavailable"}`), // retryable status
		"/health":    jsonRoute(http.StatusOK, `{"status":"ok"}`),
	})

	retry := config.DefaultRetryConfig()
	retry.InitialBackoff = time.Millisecond // the attempt count is what matters
	provider := New(providers.ProviderConfig{BaseURL: server.URL + "/v1"}, providers.ProviderOptions{
		Resilience: config.ResilienceConfig{
			Retry:          retry,
			CircuitBreaker: config.DefaultCircuitBreakerConfig(),
		},
	}).(*Provider)

	const listings = 6
	for i := range listings {
		resp, err := provider.ListModels(context.Background())
		require.NoError(t, err)

		// The listing still succeeds on meta.n_ctx_train despite /props failing.
		require.NotNil(t, resp.Data[0].Metadata)
		assert.Equal(t, 8192, *resp.Data[0].Metadata.ContextWindow, "listing #%d lost its fallback context window", i)
	}
	assert.Equal(t, listings, countPath(capture, "/props"))
	_, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, countPath(capture, "/health"))
}
