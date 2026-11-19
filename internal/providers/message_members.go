package providers

import (
	"bytes"
	"log/slog"
	"slices"

	"github.com/enterpilot/gomodel/internal/core"
)

// DropMessageMembers returns req without the named untyped members on its
// messages of the given role, for an upstream that rejects them.
//
// OpenAI returns refusal, annotations, audio and function_call on assistant
// messages, and clients append such a message to the history as it is. An
// upstream that validates messages against a closed schema then fails the
// next turn with a 400 over members the client never chose to send. Each
// adapter lists what its own upstream was seen to reject (ADR-0011): there is
// no shared list, because upstreams that accept a member must keep getting it.
//
// The request and its message list are copied only when a member is removed,
// so the caller's request never changes.
func DropMessageMembers(req *core.ChatRequest, role string, keys ...string) *core.ChatRequest {
	return dropMessageMembers(req, role, keys, false)
}

// DropNullMessageMembers is DropMessageMembers for members an upstream reads
// when they carry a value but rejects as null. A null member says the same as
// an absent one, and an OpenAI SDK message dump spells every unset member
// that way.
func DropNullMessageMembers(req *core.ChatRequest, role string, keys ...string) *core.ChatRequest {
	return dropMessageMembers(req, role, keys, true)
}

func dropMessageMembers(req *core.ChatRequest, role string, keys []string, nullOnly bool) *core.ChatRequest {
	if req == nil || len(keys) == 0 {
		return req
	}
	var adapted *core.ChatRequest
	changed := 0
	for i := range req.Messages {
		fields := req.Messages[i].ExtraFields
		if req.Messages[i].Role != role || !fields.HasAny(keys...) {
			continue
		}
		drop := keys
		if nullOnly {
			if drop = nullMembers(fields, keys); len(drop) == 0 {
				continue
			}
		}
		if adapted == nil {
			copied := *req
			copied.Messages = slices.Clone(req.Messages)
			adapted = &copied
		}
		adapted.Messages[i].ExtraFields = fields.Without(drop...)
		changed++
	}
	if adapted == nil {
		return req
	}
	slog.Debug("dropped message members the provider rejects",
		"model", req.Model, "role", role, "members", keys, "null_only", nullOnly, "messages", changed)
	return adapted
}

// nullMembers returns the keys whose member is present with a null value.
func nullMembers(fields core.UnknownJSONFields, keys []string) []string {
	var null []string
	for _, key := range keys {
		if raw := fields.Lookup(key); raw != nil && core.IsJSONNull(bytes.TrimSpace(raw)) {
			null = append(null, key)
		}
	}
	return null
}
