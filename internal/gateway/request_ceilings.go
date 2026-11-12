package gateway

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// unrankedEffort stands for an effort the common scale cannot place: it
// ranks as unknown, which a ceiling treats as exceeding.
const unrankedEffort = "?"

// ChatRequestWithCeilings returns req lowered to the ceilings: a reasoning
// effort above the ceiling becomes the ceiling, and so does an output token
// limit above it. A request that sets neither is returned as is, so a
// ceiling never turns reasoning on or raises a limit.
func ChatRequestWithCeilings(req *core.ChatRequest, ceilings requestparams.Ceilings) (*core.ChatRequest, error) {
	if req == nil || ceilings.IsZero() {
		return req, nil
	}
	if effortExceeds(req.Reasoning, req.ExtraFields, ceilings.ReasoningEffort) {
		req = ChatRequestWithReasoningEffort(req, ceilings.ReasoningEffort)
	}
	return requestparams.CapChatTokens(req, ceilings.OutputTokens)
}

// ResponsesRequestWithCeilings is ChatRequestWithCeilings for Responses.
func ResponsesRequestWithCeilings(req *core.ResponsesRequest, ceilings requestparams.Ceilings) (*core.ResponsesRequest, error) {
	if req == nil || ceilings.IsZero() {
		return req, nil
	}
	if effortExceeds(req.Reasoning, req.ExtraFields, ceilings.ReasoningEffort) {
		req = ResponsesRequestWithReasoningEffort(req, ceilings.ReasoningEffort)
	}
	return requestparams.CapResponsesTokens(req, ceilings.OutputTokens)
}

// ChatRequestExceedsCeilings reports whether the ceilings would change req.
func ChatRequestExceedsCeilings(req *core.ChatRequest, ceilings requestparams.Ceilings) bool {
	capped, err := ChatRequestWithCeilings(req, ceilings)
	return err != nil || capped != req
}

// effortExceeds reports whether a request asks for more reasoning than
// ceiling allows, in any spelling it carries. An effort the common scale
// cannot rank counts as exceeding, so no spelling slips past the ceiling.
func effortExceeds(reasoning *core.Reasoning, fields core.UnknownJSONFields, ceiling string) bool {
	if ceiling == "" {
		return false
	}
	limit := requestparams.EffortRank(ceiling)
	for _, effort := range requestedEfforts(reasoning, fields) {
		if rank := requestparams.EffortRank(effort); rank < 0 || rank > limit {
			return true
		}
	}
	return false
}

// requestedEfforts lists every reasoning effort a request sets: the common
// reasoning.effort, the flat reasoning_effort, and Gemini's thinking level
// and budget in extra_body. Each is on the common scale or unrankedEffort.
func requestedEfforts(reasoning *core.Reasoning, fields core.UnknownJSONFields) []string {
	var efforts []string
	if reasoning != nil && strings.TrimSpace(reasoning.Effort) != "" {
		efforts = append(efforts, reasoning.Effort)
	}
	if raw := fields.Lookup(flatReasoningEffortField); isSet(raw) {
		efforts = append(efforts, stringOrUnranked(raw))
	}
	return append(efforts, googleThinkingEfforts(fields.Lookup("extra_body"))...)
}

// googleThinkingEfforts reads extra_body.google.thinking_config. A thinking
// level is a level of the common scale; a zero budget turns thinking off,
// and any other budget is a token count the scale cannot rank.
func googleThinkingEfforts(extraBody json.RawMessage) []string {
	var body, google, thinking map[string]json.RawMessage
	if len(extraBody) == 0 || json.Unmarshal(extraBody, &body) != nil || json.Unmarshal(body["google"], &google) != nil || json.Unmarshal(google["thinking_config"], &thinking) != nil {
		return nil
	}
	var efforts []string
	for _, field := range googleThinkingEffortFields {
		raw := thinking[field]
		switch {
		case !isSet(raw):
		case strings.HasSuffix(strings.ToLower(field), "level"):
			efforts = append(efforts, stringOrUnranked(raw))
		case bytes.Equal(bytes.TrimSpace(raw), []byte("0")):
			efforts = append(efforts, "none")
		default:
			efforts = append(efforts, unrankedEffort)
		}
	}
	return efforts
}

func isSet(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func stringOrUnranked(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return unrankedEffort
	}
	return value
}
