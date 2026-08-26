package providers

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// AdaptReasoningEffortRequest rewrites GoModel's common nested reasoning shape
// into the flat "reasoning_effort" string extension used by several
// OpenAI-compatible providers (Gemini, DeepSeek). It shallow-copies the typed
// request and merges the effort into ExtraFields, so the body is marshaled
// only once, by the HTTP client. Other reasoning fields (e.g. budget_tokens)
// are dropped: these providers accept the flat string only.
func AdaptReasoningEffortRequest(req *core.ChatRequest, effort string) (*core.ChatRequest, error) {
	adapted := *req
	adapted.Reasoning = nil
	encodedEffort, err := json.Marshal(effort)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"reasoning_effort": encodedEffort,
	})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
	}
	adapted.ExtraFields = extra
	return &adapted, nil
}

// ResolveReasoningEffort returns the reasoning effort a client asked for,
// accepting both GoModel's nested reasoning object and the flat
// "reasoning_effort" string that OpenAI Chat Completions clients send (coding
// agents built on the OpenAI-compatible SDKs use the flat form). The nested
// object wins when it carries a value; an empty object expresses no intent, so
// the flat string still applies. Values are trimmed and lowercased so
// spellings like " High " reach provider mappings as "high".
func ResolveReasoningEffort(req *core.ChatRequest) string {
	if req == nil {
		return ""
	}
	if req.Reasoning != nil {
		if effort := NormalizeReasoningEffortInput(req.Reasoning.Effort); effort != "" {
			return effort
		}
	}
	raw := bytes.TrimSpace(req.ExtraFields.Lookup("reasoning_effort"))
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var effort string
	if err := json.Unmarshal(raw, &effort); err != nil {
		return ""
	}
	return NormalizeReasoningEffortInput(effort)
}

// NormalizeReasoningEffortInput canonicalizes a user-supplied effort spelling
// so exact-match provider mappings do not downgrade values like " HIGH " to
// their fallback level.
func NormalizeReasoningEffortInput(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
}
