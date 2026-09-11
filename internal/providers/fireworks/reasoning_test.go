package fireworks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestChatCompletion_MapsReasoningToReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "effort", effort: "low", wantEffort: "low"},
		{name: "empty effort drops it"},
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
			provider := NewWithHTTPClient("test-api-key", server.URL, nil, llmclient.Hooks{})

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     "accounts/fireworks/models/glm-5p2",
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
