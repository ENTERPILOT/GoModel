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
