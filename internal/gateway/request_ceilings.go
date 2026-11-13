package gateway

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// ChatRequestWithCeilings returns req lowered to the ceilings: a reasoning
// effort above the ceiling becomes the ceiling, and so does an output token
// limit above it. A request without an output token limit gets the token
// ceiling as its limit; one without an effort keeps none, so a ceiling never
// turns reasoning on.
func ChatRequestWithCeilings(req *core.ChatRequest, ceilings requestparams.Ceilings) (*core.ChatRequest, error) {
	if req == nil || ceilings.IsZero() {
		return req, nil
	}
	if effortExceeds(req.Reasoning, req.ExtraFields, ceilings.ReasoningEffort) {
		req = chatRequestWithEffort(req, ceilings.ReasoningEffort, withoutReasoningControls)
	}
	return requestparams.CapChatTokens(req, ceilings.OutputTokens)
}

// ResponsesRequestWithCeilings is ChatRequestWithCeilings for Responses.
func ResponsesRequestWithCeilings(req *core.ResponsesRequest, ceilings requestparams.Ceilings) (*core.ResponsesRequest, error) {
	if req == nil || ceilings.IsZero() {
		return req, nil
	}
	if effortExceeds(req.Reasoning, req.ExtraFields, ceilings.ReasoningEffort) {
		req = responsesRequestWithEffort(req, ceilings.ReasoningEffort, withoutReasoningControls)
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
