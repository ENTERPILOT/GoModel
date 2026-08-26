package opencodego

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// captureBody serves a minimal chat completion and records the outgoing body.
func captureBody(t *testing.T, body *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, body); err != nil {
			t.Errorf("unmarshal request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-opencode",
			"created":1677652288,
			"model":"ox-alpha-free",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	}))
}

func TestChatCompletion_InjectsDefaultReasoningEffortWhenAbsent(t *testing.T) {
	var got map[string]any
	server := captureBody(t, &got)
	defer server.Close()

	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "ox-alpha-free",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got["reasoning_effort"])
	}
	if _, ok := got["reasoning"]; ok {
		t.Fatal("nested reasoning object should not be forwarded")
	}
}

func TestChatCompletion_MapsExplicitReasoningEffort(t *testing.T) {
	tests := []struct {
		name   string
		effort string
		want   string
	}{
		{name: "low passes through", effort: "low", want: "low"},
		{name: "medium downgrades", effort: "medium", want: "low"},
		{name: "none becomes low", effort: "none", want: "low"},
		{name: "high passes through", effort: "high", want: "high"},
		{name: "xhigh maps to max", effort: "xhigh", want: "max"},
		{name: "max passes through", effort: "MAX", want: "max"},
		{name: "unknown level passes through", effort: "turbo", want: "turbo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			server := captureBody(t, &got)
			defer server.Close()

			_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     "ox-alpha-free",
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if got["reasoning_effort"] != tt.want {
				t.Fatalf("reasoning_effort = %v, want %v", got["reasoning_effort"], tt.want)
			}
		})
	}
}

func TestChatCompletion_KeepsClientSuppliedFlatReasoningEffort(t *testing.T) {
	var got map[string]any
	server := captureBody(t, &got)
	defer server.Close()

	extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
		"reasoning_effort": json.RawMessage(`"max"`),
	})
	if err != nil {
		t.Fatalf("MergeUnknownJSONFields() error = %v", err)
	}

	if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "ox-alpha-free",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: extra,
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %v, want max (client value preserved)", got["reasoning_effort"])
	}
}

func TestChatCompletion_DefaultReasoningEffortEnvOverride(t *testing.T) {
	t.Setenv(defaultReasoningEffortEnvVar, "high")

	var got map[string]any
	server := captureBody(t, &got)
	defer server.Close()

	if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "ox-alpha-free",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", got["reasoning_effort"])
	}
}

func TestChatCompletion_DefaultReasoningEffortDisabled(t *testing.T) {
	for _, value := range []string{"none", "OFF"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(defaultReasoningEffortEnvVar, value)

			var got map[string]any
			server := captureBody(t, &got)
			defer server.Close()

			if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "ox-alpha-free",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			}); err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if _, ok := got["reasoning_effort"]; ok {
				t.Fatalf("reasoning_effort = %v, want absent when injection is disabled", got["reasoning_effort"])
			}
		})
	}
}

func TestStreamChatCompletion_InjectsDefaultReasoningEffort(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("unmarshal request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	body, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "ox-alpha-free",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()

	if got["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got["reasoning_effort"])
	}
}

// TestChatCompletion_MessagesModelUnaffected pins that the injection lives on
// the /chat/completions path only: the Anthropic-native dialect has no
// reasoning_effort field.
func TestChatCompletion_MessagesModelUnaffected(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("unmarshal request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_opencode",
			"model":"qwen3.7-max",
			"content":[{"type":"text","text":"hello"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":5,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3.7-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Fatalf("reasoning_effort = %v, want absent on the /messages dialect", got["reasoning_effort"])
	}
}

func TestAdaptChatRequest_NilRequest(t *testing.T) {
	req, err := adaptChatRequest(defaultReasoningEffort)(nil)
	if err != nil {
		t.Fatalf("adaptChatRequest() error = %v", err)
	}
	if req != nil {
		t.Fatalf("req = %#v, want nil", req)
	}
}

func TestLoadDefaultReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "unset uses default", env: "", want: "low"},
		{name: "override normalized", env: " XHIGH ", want: "max"},
		{name: "none disables", env: "none", want: ""},
		{name: "off disables", env: "off", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(defaultReasoningEffortEnvVar, tt.env)
			if got := loadDefaultReasoningEffort(); got != tt.want {
				t.Fatalf("loadDefaultReasoningEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}
