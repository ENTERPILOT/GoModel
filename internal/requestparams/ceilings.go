package requestparams

import (
	"bytes"
	"maps"
	"slices"
	"strconv"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Ceilings are upper bounds on what one provider attempt may ask for. A
// ceiling lowers a request's value and never raises it. The token ceiling
// also fills in a limit a request leaves out, since an omitted limit is no
// limit at all; the effort ceiling adds nothing, so it never switches
// reasoning on. Zero values mean no ceiling.
type Ceilings struct {
	// ReasoningEffort is the highest level of ReasoningEffortLevels allowed.
	ReasoningEffort string
	// OutputTokens is the highest output token limit allowed, in any of the
	// token-limit spellings.
	OutputTokens int
}

// IsZero reports whether no ceiling is set.
func (c Ceilings) IsZero() bool {
	return c.ReasoningEffort == "" && c.OutputTokens <= 0
}

// Tighten returns the lower of each ceiling in c and other, so combining
// ceilings can only narrow them.
func (c Ceilings) Tighten(other Ceilings) Ceilings {
	if other.ReasoningEffort != "" && (c.ReasoningEffort == "" || EffortRank(other.ReasoningEffort) < EffortRank(c.ReasoningEffort)) {
		c.ReasoningEffort = other.ReasoningEffort
	}
	if other.OutputTokens > 0 && (c.OutputTokens <= 0 || other.OutputTokens < c.OutputTokens) {
		c.OutputTokens = other.OutputTokens
	}
	return c
}

// CapChatTokens returns req with its output token limit within limit. Adapters
// read max_tokens and, where that is unset, max_completion_tokens, so a
// request that sets neither gets max_tokens: its own max_output_tokens when
// that is within limit (chat has no such member, so alone it would limit
// nothing upstream), limit otherwise. Every spelling the request does carry
// is lowered to limit in place. The request is never mutated: the result is
// req itself when it is already within the limit.
func CapChatTokens(req *core.ChatRequest, limit int) (*core.ChatRequest, error) {
	if req == nil || limit <= 0 {
		return req, nil
	}
	if respelled := req.ExtraFields.Respelled(tokenLimitSpellings...); len(respelled) > 0 {
		cloned := *req
		cloned.ExtraFields = req.ExtraFields.Without(respelled...)
		req = &cloned
	}
	if req.MaxTokens == nil && !carriesTokenLimit(req.ExtraFields, "max_completion_tokens") {
		return ApplyChat(req, nil, forcedTokenLimit("max_tokens", lowestTokenLimit(req.ExtraFields, limit, chatTokenLimitExtras)))
	}
	maxTokens, fields, changed, err := capTokens(req.MaxTokens, req.ExtraFields, limit, chatTokenLimitExtras)
	if err != nil || !changed {
		return req, err
	}
	cloned := *req
	cloned.MaxTokens, cloned.ExtraFields = maxTokens, fields
	return &cloned, nil
}

// CapResponsesTokens is CapChatTokens for the Responses API, whose only
// limit is max_output_tokens: a request that sets it in a chat spelling
// alone gets it there.
func CapResponsesTokens(req *core.ResponsesRequest, limit int) (*core.ResponsesRequest, error) {
	if req == nil || limit <= 0 {
		return req, nil
	}
	if respelled := req.ExtraFields.Respelled(tokenLimitSpellings...); len(respelled) > 0 {
		cloned := *req
		cloned.ExtraFields = req.ExtraFields.Without(respelled...)
		req = &cloned
	}
	if req.MaxOutputTokens == nil {
		return ApplyResponses(req, nil, forcedTokenLimit("max_output_tokens", lowestTokenLimit(req.ExtraFields, limit, responsesTokenLimitExtras)))
	}
	maxTokens, fields, changed, err := capTokens(req.MaxOutputTokens, req.ExtraFields, limit, responsesTokenLimitExtras)
	if err != nil || !changed {
		return req, err
	}
	cloned := *req
	cloned.MaxOutputTokens, cloned.ExtraFields = maxTokens, fields
	return &cloned, nil
}

// tokenLimitSpellings are the members a cap bounds. One spelled in another
// letter case (MAX_TOKENS) is dropped from a capped request: the gateway
// already read it as the typed limit where it is one, and an upstream that
// matches names case-insensitively would read it over the capped member.
var tokenLimitSpellings = slices.Collect(maps.Keys(tokenLimitKeys))

// carriesTokenLimit reports whether fields set key to anything but null,
// which is no limit.
func carriesTokenLimit(fields core.UnknownJSONFields, key string) bool {
	for _, raw := range fields.LookupAll(key) {
		if !core.IsJSONNull(bytes.TrimSpace(raw)) {
			return true
		}
	}
	return false
}

// lowestTokenLimit returns the lowest limit within limit that fields carry
// under keys, or limit when they carry none.
func lowestTokenLimit(fields core.UnknownJSONFields, limit int, keys []string) int {
	for _, key := range keys {
		if value, _ := boundedTokenLimit(fields.LookupAll(key), limit); value > 0 {
			limit = value
		}
	}
	return limit
}

// forcedTokenLimit is the parameter set that writes limit as the request's
// only token limit: forcing it strips every other spelling the client sent.
func forcedTokenLimit(key string, limit int) Set {
	return Set{key: json.RawMessage(strconv.Itoa(limit))}
}

// capTokens brings the typed token limit and every spelling of it among keys
// in fields within limit. It returns the values a capped copy of the request
// carries, and whether anything changed.
func capTokens(typed *int, fields core.UnknownJSONFields, limit int, keys []string) (*int, core.UnknownJSONFields, bool, error) {
	changed := typed != nil && (*typed < 1 || *typed > limit)
	if changed {
		typed = &limit
	}
	var dropped []string
	var replaced map[string]json.RawMessage
	for _, key := range keys {
		value, rewrite := boundedTokenLimit(fields.LookupAll(key), limit)
		switch {
		case !rewrite:
		case value == 0:
			dropped = append(dropped, key)
		default:
			if replaced == nil {
				replaced = make(map[string]json.RawMessage, len(keys))
			}
			replaced[key] = json.RawMessage(strconv.Itoa(value))
		}
	}
	if len(dropped) == 0 && len(replaced) == 0 {
		return typed, fields, changed, nil
	}
	merged, err := core.MergeUnknownJSONFields(fields.Without(dropped...), replaced)
	if err != nil {
		return nil, fields, false, err
	}
	return typed, merged, true, nil
}

// boundedTokenLimit returns what one token-limit spelling carries once
// capped. Only a whole number from 1 to limit is within the cap: adapters
// and upstreams read anything else (a number below 1, a fraction, a string)
// as no limit, or as unlimited. rewrite is false when every occurrence of
// the member is within the cap. Otherwise value replaces them all: the
// lowest occurrence within the cap, limit when there is none, or 0, which
// drops a member that is only null (no limit) so no adapter prefers it to
// the spelling that is set.
func boundedTokenLimit(values []json.RawMessage, limit int) (value int, rewrite bool) {
	set := false
	for _, raw := range values {
		raw = bytes.TrimSpace(raw)
		if core.IsJSONNull(raw) {
			rewrite = true
			continue
		}
		set = true
		n, err := strconv.Atoi(string(raw))
		if err != nil || n < 1 || n > limit {
			rewrite = true
			continue
		}
		if value == 0 || n < value {
			value = n
		}
	}
	if set && value == 0 {
		value = limit
	}
	return value, rewrite
}
