package gemini

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Gemini attaches a thoughtSignature to the part that carries a functionCall.
// Gemini 3 validates it strictly: replaying a functionCall part without the
// signature it was returned with fails the whole request with HTTP 400
// ("Function call is missing a thought_signature in functionCall parts").
//
// The OpenAI-compatible surface has no field for it, so the signature travels
// on the tool call as extra_content.google.thought_signature — the same member
// Google's own OpenAI-compatible endpoint uses, so clients that already
// round-trip Gemini tool calls need no change.
const (
	extraContentField     = "extra_content"
	thoughtSignatureField = "thought_signature"
)

// geminiExtraContent is the extra_content payload shape: a per-vendor object
// holding fields with no OpenAI-compatible equivalent.
type geminiExtraContent struct {
	Google struct {
		ThoughtSignature string `json:"thought_signature,omitempty"`
	} `json:"google"`
}

// toolCallThoughtSignature reads the signature a client replayed with an
// assistant tool call. The canonical spelling is
// extra_content.google.thought_signature; a flat thought_signature (either
// case) on the tool call or on its function object is accepted too, because
// other gateways and SDKs emit the signature that way.
func toolCallThoughtSignature(call core.ToolCall) string {
	for _, fields := range [...]core.UnknownJSONFields{call.ExtraFields, call.Function.ExtraFields} {
		if signature := thoughtSignatureFromFields(fields); signature != "" {
			return signature
		}
	}
	return ""
}

func thoughtSignatureFromFields(fields core.UnknownJSONFields) string {
	if raw := fields.Lookup(extraContentField); len(raw) > 0 {
		var extra geminiExtraContent
		if err := json.Unmarshal(raw, &extra); err == nil && extra.Google.ThoughtSignature != "" {
			return extra.Google.ThoughtSignature
		}
	}
	for _, key := range [...]string{thoughtSignatureField, "thoughtSignature"} {
		raw := fields.Lookup(key)
		if len(raw) == 0 {
			continue
		}
		var signature string
		if err := json.Unmarshal(raw, &signature); err == nil && signature != "" {
			return signature
		}
	}
	return ""
}

// thoughtSignatureExtraFields returns the tool-call extras that carry signature
// back to the client. An empty signature leaves the extras untouched so tool
// calls without one stay byte-identical to before.
func thoughtSignatureExtraFields(signature string) core.UnknownJSONFields {
	if signature == "" {
		return core.UnknownJSONFields{}
	}
	var extra geminiExtraContent
	extra.Google.ThoughtSignature = signature
	payload, err := json.Marshal(extra)
	if err != nil {
		return core.UnknownJSONFields{}
	}
	return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{extraContentField: payload})
}
