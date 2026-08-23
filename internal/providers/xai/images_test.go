package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// TestCreateImage verifies the xAI provider advertises image generation and
// delegates it to the OpenAI-compatible /images/generations endpoint: the
// request is POSTed with every field forwarded verbatim (xAI has no
// image-specific parameter mapping), upstream errors propagate, and sparse
// upstream responses are normalized to the OpenAI envelope.
func TestCreateImage(t *testing.T) {
	n := 2
	tests := []struct {
		name       string
		req        *core.ImageGenerationRequest
		statusCode int
		body       string
		wantErr    string
		wantBody   map[string]any // forwarded JSON fields that must be present
		check      func(*testing.T, *core.ImageGenerationResponse)
	}{
		{
			name:       "forwards required and optional fields",
			req:        &core.ImageGenerationRequest{Model: "grok-imagine-image", Prompt: "A cat", N: &n, ResponseFormat: "url"},
			statusCode: http.StatusOK,
			body:       `{"created":1713833628,"data":[{"url":"https://imgen.x.ai/xai-imgen/img.jpg"},{"url":"https://imgen.x.ai/xai-imgen/img2.jpg"}]}`,
			wantBody:   map[string]any{"model": "grok-imagine-image", "prompt": "A cat", "n": float64(2), "response_format": "url"},
			check: func(t *testing.T, resp *core.ImageGenerationResponse) {
				if resp.Created != 1713833628 || len(resp.Data) != 2 || resp.Data[0].URL != "https://imgen.x.ai/xai-imgen/img.jpg" {
					t.Errorf("response = %+v", resp)
				}
			},
		},
		{
			name:       "normalizes sparse response",
			req:        &core.ImageGenerationRequest{Model: "grok-imagine-image", Prompt: "A cat"},
			statusCode: http.StatusOK,
			body:       `{}`,
			check: func(t *testing.T, resp *core.ImageGenerationResponse) {
				if resp.Created == 0 {
					t.Error("Created should default to now when upstream omits it")
				}
				if resp.Data == nil {
					t.Error("Data should be an empty array, not null")
				}
			},
		},
		{
			name:       "propagates upstream error",
			req:        &core.ImageGenerationRequest{Model: "grok-imagine-image", Prompt: "A cat", Size: "1024x1024"},
			statusCode: http.StatusBadRequest,
			body:       `{"error":"Argument not supported: size"}`,
			wantErr:    "size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
					t.Errorf("Authorization header should start with 'Bearer '")
				}
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			var imager core.ImageProvider = provider
			resp, err := imager.CreateImage(context.Background(), tt.req)

			if gotMethod != http.MethodPost || gotPath != "/images/generations" {
				t.Errorf("request = %s %s, want POST /images/generations", gotMethod, gotPath)
			}
			for key, want := range tt.wantBody {
				if got := gotBody[key]; got != want {
					t.Errorf("forwarded[%q] = %v, want %v", key, got, want)
				}
			}

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want message containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateImage() error = %v", err)
			}
			tt.check(t, resp)
		})
	}
}
