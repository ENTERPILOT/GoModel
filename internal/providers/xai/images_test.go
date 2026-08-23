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
// delegates it to the OpenAI-compatible /images/generations endpoint.
func TestCreateImage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("Authorization header should start with 'Bearer '")
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"url":"https://imgen.x.ai/xai-imgen/img.jpg","revised_prompt":"A cat on a windowsill"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	var imager core.ImageProvider = provider
	resp, err := imager.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "grok-2-image", Prompt: "A cat"})
	if err != nil {
		t.Fatalf("CreateImage() error = %v", err)
	}

	if gotPath != "/images/generations" {
		t.Errorf("path = %q, want /images/generations", gotPath)
	}
	if gotBody["model"] != "grok-2-image" || gotBody["prompt"] != "A cat" {
		t.Errorf("forwarded body = %v", gotBody)
	}
	if resp.Created != 1713833628 || len(resp.Data) != 1 || resp.Data[0].URL != "https://imgen.x.ai/xai-imgen/img.jpg" {
		t.Errorf("response = %+v", resp)
	}
}
