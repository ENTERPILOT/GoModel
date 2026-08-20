package llamacpp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

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
			name:              "supported modalities become capabilities",
			listing:           legacyListing,
			propsStatus:       http.StatusOK,
			props:             `{"default_generation_settings":{"n_ctx":4096},"modalities":{"vision":true,"video":true,"audio":false}}`,
			wantContextWindow: 4096,
			wantCapabilities:  map[string]bool{"vision": true, "video": true},
			wantPropsFetched:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			propsFetched := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/models":
					_, _ = w.Write([]byte(tt.listing))
				case "/props":
					propsFetched = true
					w.WriteHeader(tt.propsStatus)
					_, _ = w.Write([]byte(tt.props))
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

			resp, err := provider.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels() error = %v", err)
			}
			if len(resp.Data) != 1 {
				t.Fatalf("len(resp.Data) = %d, want 1", len(resp.Data))
			}
			if propsFetched != tt.wantPropsFetched {
				t.Fatalf("props fetched = %v, want %v", propsFetched, tt.wantPropsFetched)
			}

			model := resp.Data[0]
			wantID := tt.wantModelID
			if wantID == "" {
				wantID = "Meta-Llama-3.1-8B-Instruct"
			}
			if model.ID != wantID {
				t.Fatalf("model.ID = %q, want %q", model.ID, wantID)
			}
			if model.Metadata == nil {
				t.Fatalf("model.Metadata = nil, want context window %d", tt.wantContextWindow)
			}
			if model.Metadata.ContextWindow == nil || *model.Metadata.ContextWindow != tt.wantContextWindow {
				t.Fatalf("context window = %v, want %d", model.Metadata.ContextWindow, tt.wantContextWindow)
			}
			if len(model.Metadata.Capabilities) != len(tt.wantCapabilities) {
				t.Fatalf("capabilities = %v, want %v", model.Metadata.Capabilities, tt.wantCapabilities)
			}
			for name, want := range tt.wantCapabilities {
				if model.Metadata.Capabilities[name] != want {
					t.Fatalf("capability %q = %v, want %v", name, model.Metadata.Capabilities[name], want)
				}
			}
			// Modes must stay empty so the registry's ID heuristic can still
			// classify local embedding and reranking GGUFs.
			if len(model.Metadata.Modes) != 0 {
				t.Fatalf("modes = %v, want none", model.Metadata.Modes)
			}
		})
	}
}

func TestListModels_RouterModeKeepsPerModelContextAndSkipsProps(t *testing.T) {
	propsFetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{
				"object":"list",
				"data":[
					{"id":"gemma-3-4b","object":"model","meta":{"n_ctx":8192,"n_ctx_train":131072}},
					{"id":"qwen3-8b","object":"model","meta":{"n_ctx":32768,"n_ctx_train":262144}}
				]
			}`))
		case "/props":
			propsFetched = true
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":512}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if propsFetched {
		t.Fatal("props was fetched for a multi-model listing; it describes a single loaded model")
	}

	want := map[string]int{"gemma-3-4b": 8192, "qwen3-8b": 32768}
	for _, model := range resp.Data {
		if model.Metadata == nil || model.Metadata.ContextWindow == nil {
			t.Fatalf("model %q lost its context window", model.ID)
		}
		if got := *model.Metadata.ContextWindow; got != want[model.ID] {
			t.Fatalf("model %q context window = %d, want %d", model.ID, got, want[model.ID])
		}
	}
}

func TestListModels_LeavesMetadataUnsetWhenServerReportsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			// LM Studio and other plain OpenAI-compatible servers omit "meta".
			_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":0}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(resp.Data) = %d, want 1", len(resp.Data))
	}
	if resp.Object != "list" {
		t.Fatalf("resp.Object = %q, want list", resp.Object)
	}
	if resp.Data[0].Object != "model" {
		t.Fatalf("model.Object = %q, want model", resp.Data[0].Object)
	}
	if resp.Data[0].Metadata != nil {
		t.Fatalf("model.Metadata = %+v, want nil so lower metadata layers still apply", resp.Data[0].Metadata)
	}
}
