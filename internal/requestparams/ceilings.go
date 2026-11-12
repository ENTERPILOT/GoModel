package requestparams

import (
	"bytes"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Ceilings are upper bounds on what one provider attempt may ask for. A
// ceiling lowers a request's value and never raises it: a request that sets
// no value is left alone. Zero values mean no ceiling.
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

// CapChatTokens returns req with every output token limit above limit lowered
// to it, in the spelling the client used. The request is never mutated: the
// result is req itself when nothing exceeds the limit.
func CapChatTokens(req *core.ChatRequest, limit int) (*core.ChatRequest, error) {
	if req == nil || limit <= 0 {
		return req, nil
	}
	maxTokens, fields, changed, err := capTokens(req.MaxTokens, req.ExtraFields, limit, chatTokenLimitExtras)
	if err != nil || !changed {
		return req, err
	}
	cloned := *req
	cloned.MaxTokens, cloned.ExtraFields = maxTokens, fields
	return &cloned, nil
}

// CapResponsesTokens is CapChatTokens for the Responses API.
func CapResponsesTokens(req *core.ResponsesRequest, limit int) (*core.ResponsesRequest, error) {
	if req == nil || limit <= 0 {
		return req, nil
	}
	maxTokens, fields, changed, err := capTokens(req.MaxOutputTokens, req.ExtraFields, limit, responsesTokenLimitExtras)
	if err != nil || !changed {
		return req, err
	}
	cloned := *req
	cloned.MaxOutputTokens, cloned.ExtraFields = maxTokens, fields
	return &cloned, nil
}

// capTokens lowers the typed token limit and every spelling of it among keys
// in fields to limit. It returns the values a capped copy of the request
// carries, and whether anything exceeded limit.
func capTokens(typed *int, fields core.UnknownJSONFields, limit int, keys []string) (*int, core.UnknownJSONFields, bool, error) {
	typedExceeds := typed != nil && *typed > limit
	extras, err := cappedExtras(fields, limit, keys)
	if err != nil || (!typedExceeds && len(extras) == 0) {
		return typed, fields, false, err
	}
	if typedExceeds {
		typed = &limit
	}
	merged, err := core.MergeUnknownJSONFields(fields, extras)
	if err != nil {
		return nil, fields, false, err
	}
	return typed, merged, true, nil
}

// cappedExtras returns the replacement for each token-limit spelling in
// fields that exceeds limit. A value that is not a number cannot be compared,
// so it is replaced too rather than let past the ceiling.
func cappedExtras(fields core.UnknownJSONFields, limit int, keys []string) (map[string]json.RawMessage, error) {
	var capped map[string]json.RawMessage
	for _, key := range keys {
		raw := bytes.TrimSpace(fields.Lookup(key))
		if len(raw) == 0 || bytes.Equal(raw, []byte(nullLexeme)) {
			continue
		}
		var value float64
		if err := json.Unmarshal(raw, &value); err == nil && value <= float64(limit) {
			continue
		}
		encoded, err := json.Marshal(limit)
		if err != nil {
			return nil, err
		}
		if capped == nil {
			capped = make(map[string]json.RawMessage, len(keys))
		}
		capped[key] = encoded
	}
	return capped, nil
}
