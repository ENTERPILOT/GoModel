package providers

import (
	"log/slog"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// CapStopSequences returns req with its "stop" list truncated to limit entries.
// Clients may legitimately send more: Anthropic's Messages API accepts a long
// stop_sequences list, and the gateway maps it onto the chat "stop" field, but
// OpenAI-family chat APIs reject a list longer than four outright. Truncating
// keeps the request working on every provider (Postel's law) instead of
// failing it upstream over a field the client cannot know the limit of. The
// dropped sequences are logged so the behavior is visible to operators.
//
// The request is shallow-copied like the other request adapters; a "stop"
// value that is absent, a bare string, or already short enough is left alone.
func CapStopSequences(req *core.ChatRequest, limit int) (*core.ChatRequest, error) {
	if req == nil || limit <= 0 {
		return req, nil
	}
	raw := req.ExtraFields.Lookup("stop")
	if len(raw) == 0 {
		return req, nil
	}
	var sequences []json.RawMessage
	if err := json.Unmarshal(raw, &sequences); err != nil || len(sequences) <= limit {
		// Not a list (a bare string is valid too) or within the limit: the
		// upstream decides. A malformed value is reported by the provider.
		return req, nil
	}

	capped, err := json.Marshal(sequences[:limit])
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt stop sequences: "+err.Error(), err)
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{"stop": capped})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt stop sequences: "+err.Error(), err)
	}
	slog.Warn("truncated stop sequences to the provider limit",
		"model", req.Model, "limit", limit, "requested", len(sequences))

	adapted := *req
	adapted.ExtraFields = extra
	return &adapted, nil
}
