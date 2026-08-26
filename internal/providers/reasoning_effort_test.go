package providers

import (
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestAdaptReasoningEffortRequest(t *testing.T) {
	req := &core.ChatRequest{
		Model:     "some-model",
		Reasoning: &core.Reasoning{Effort: "high"},
	}

	adapted, err := AdaptReasoningEffortRequest(req, "high")
	if err != nil {
		t.Fatalf("AdaptReasoningEffortRequest: %v", err)
	}

	if adapted.Reasoning != nil {
		t.Fatalf("adapted.Reasoning = %#v, want nil (flat extension only)", adapted.Reasoning)
	}
	if req.Reasoning == nil {
		t.Fatal("original request mutated: Reasoning cleared")
	}

	body, err := json.Marshal(adapted)
	if err != nil {
		t.Fatalf("marshal adapted request: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal wire body: %v", err)
	}
	if got := string(wire["reasoning_effort"]); got != `"high"` {
		t.Fatalf("reasoning_effort = %s, want \"high\"", got)
	}
	if _, present := wire["reasoning"]; present {
		t.Fatal("reasoning present on the wire, want dropped")
	}
}

func TestAdaptReasoningEffortRequestPreservesExistingExtraFields(t *testing.T) {
	req := &core.ChatRequest{
		Model:     "some-model",
		Reasoning: &core.Reasoning{Effort: "low"},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"custom_field":     json.RawMessage(`"kept"`),
			"reasoning_effort": json.RawMessage(`"stale"`),
		}),
	}

	adapted, err := AdaptReasoningEffortRequest(req, "low")
	if err != nil {
		t.Fatalf("AdaptReasoningEffortRequest: %v", err)
	}

	body, err := json.Marshal(adapted)
	if err != nil {
		t.Fatalf("marshal adapted request: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal wire body: %v", err)
	}
	if got := string(wire["custom_field"]); got != `"kept"` {
		t.Fatalf("custom_field = %s, want preserved", got)
	}
	if got := string(wire["reasoning_effort"]); got != `"low"` {
		t.Fatalf("reasoning_effort = %s, want adaptation to win over stale extra field", got)
	}
}

func TestResolveReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		req  *core.ChatRequest
		want string
	}{
		{name: "nil request", req: nil, want: ""},
		{name: "no reasoning", req: &core.ChatRequest{Model: "m"}, want: ""},
		{
			name: "nested object",
			req:  &core.ChatRequest{Reasoning: &core.Reasoning{Effort: "high"}},
			want: "high",
		},
		{
			name: "flat chat completions field",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`"medium"`),
			})},
			want: "medium",
		},
		{
			name: "nested wins over flat",
			req: &core.ChatRequest{
				Reasoning: &core.Reasoning{Effort: "low"},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_effort": json.RawMessage(`"high"`),
				}),
			},
			want: "low",
		},
		{
			name: "empty nested object falls back to flat",
			req: &core.ChatRequest{
				Reasoning: &core.Reasoning{},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_effort": json.RawMessage(`"high"`),
				}),
			},
			want: "high",
		},
		{
			name: "spelling normalized",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`" XHigh "`),
			})},
			want: "xhigh",
		},
		{
			name: "whitespace-only nested effort falls back to flat",
			req: &core.ChatRequest{
				Reasoning: &core.Reasoning{Effort: "  "},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_effort": json.RawMessage(`"medium"`),
				}),
			},
			want: "medium",
		},
		{
			name: "whitespace-only flat field",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`"  "`),
			})},
			want: "",
		},
		{
			name: "null flat field",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`null`),
			})},
			want: "",
		},
		{
			name: "non-string flat field",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`{"effort":"high"}`),
			})},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveReasoningEffort(tt.req); got != tt.want {
				t.Fatalf("ResolveReasoningEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}
