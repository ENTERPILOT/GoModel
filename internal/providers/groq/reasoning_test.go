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
		{name: "gpt-oss cannot turn reasoning off", model: "openai/gpt-oss-20b", effort: "none", wantEffort: "low"},
		{name: "gpt-oss minimal", model: "openai/gpt-oss-20b", effort: "minimal", wantEffort: "low"},
		{name: "qwen3.6 turns reasoning on", model: "qwen/qwen3.6-27b", effort: "medium", wantEffort: "default"},
		{name: "qwen3.6 keeps none", model: "qwen/qwen3.6-27b", effort: "none", wantEffort: "none"},
		{name: "qwen3.8 keeps the level", model: "qwen/qwen3.8-27b", effort: "medium", wantEffort: "medium"},
		{name: "qwen3.8 keeps none", model: "qwen/qwen3.8-27b", effort: "none", wantEffort: "none"},
		{name: "qwen3.8 caps max", model: "qwen/qwen3.8-27b", effort: "max", wantEffort: "high"},
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

// Groq rejects more than four stop sequences; the Messages API accepts more,
// so the list is truncated instead of failing the request upstream.
func TestChatCompletion_CapsStopSequences(t *testing.T) {
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

	extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
		"stop": json.RawMessage(`["a","b","c","d","e","f"]`),
	})
	if err != nil {
		t.Fatalf("MergeUnknownJSONFields: %v", err)
	}
	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "openai/gpt-oss-20b",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: extra,
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	stop, _ := raw["stop"].([]any)
	if len(stop) != maxStopSequences {
		t.Fatalf("stop = %v, want %d entries", raw["stop"], maxStopSequences)
	}
	if stop[0] != "a" || stop[3] != "d" {
		t.Errorf("stop = %v, want the first four sequences", stop)
	}
}

func TestChatCompletion_DefaultsReasoningFormatPerModelFamily(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		caller     string // caller-supplied reasoning_format, "" for none
		wantFormat any    // nil means the field must be absent
	}{
		{name: "qwen3 gets parsed so <think> is not the answer", model: "qwen/qwen3.6-27b", wantFormat: "parsed"},
		{name: "gpt-oss gets parsed", model: "openai/gpt-oss-20b", wantFormat: "parsed"},
		{name: "caller choice wins", model: "qwen/qwen3.6-27b", caller: "raw", wantFormat: "raw"},
		{name: "compound rejects the field", model: "groq/compound-mini"},
		{name: "plain chat models are left alone", model: "llama-3.3-70b-versatile"},
		{name: "whisper is left alone", model: "whisper-large-v3-turbo"},
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

			req := &core.ChatRequest{Model: tt.model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
			if tt.caller != "" {
				extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
					"reasoning_format": json.RawMessage(`"` + tt.caller + `"`),
				})
				if err != nil {
					t.Fatalf("MergeUnknownJSONFields() error = %v", err)
				}
				req.ExtraFields = extra
			}
			if _, err := provider.ChatCompletion(context.Background(), req); err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			got, ok := raw["reasoning_format"]
			if tt.wantFormat == nil {
				if ok {
					t.Errorf("reasoning_format = %v, want absent", got)
				}
				return
			}
			if got != tt.wantFormat {
				t.Errorf("reasoning_format = %v, want %v", got, tt.wantFormat)
			}
		})
	}
}
