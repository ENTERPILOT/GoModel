package groq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestChatCompletion_MapsReasoningPerModelFamily(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "gpt-oss keeps the effort", model: "openai/gpt-oss-20b", effort: "low", wantEffort: "low"},
		{name: "gpt-oss caps max", model: "openai/gpt-oss-120b", effort: "max", wantEffort: "high"},
		{name: "qwen3 turns reasoning on", model: "qwen/qwen3.6-27b", effort: "medium", wantEffort: "default"},
		{name: "other models drop it", model: "llama-3.3-70b-versatile", effort: "high"},
		{name: "empty effort drops it", model: "openai/gpt-oss-20b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if _, ok := raw["reasoning"]; ok {
				t.Errorf("request body includes nested reasoning: %v", raw["reasoning"])
			}
			got, ok := raw["reasoning_effort"]
			if tt.wantEffort == "" {
				if ok {
					t.Errorf("reasoning_effort = %v, want absent", got)
				}
			} else if got != tt.wantEffort {
				t.Errorf("reasoning_effort = %v, want %q", got, tt.wantEffort)
			}
		})
	}
}
