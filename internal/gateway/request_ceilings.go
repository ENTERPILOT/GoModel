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
	if effort, exceeds := effortWithinCeiling(req.Reasoning, req.ExtraFields, ceilings.ReasoningEffort); exceeds {
		req = chatRequestWithEffort(req, effort, withoutReasoningControls)
	}
	return requestparams.CapChatTokens(req, ceilings.OutputTokens)
}

// ResponsesRequestWithCeilings is ChatRequestWithCeilings for Responses.
func ResponsesRequestWithCeilings(req *core.ResponsesRequest, ceilings requestparams.Ceilings) (*core.ResponsesRequest, error) {
	if req == nil || ceilings.IsZero() {
		return req, nil
	}
	if effort, exceeds := effortWithinCeiling(req.Reasoning, req.ExtraFields, ceilings.ReasoningEffort); exceeds {
		req = responsesRequestWithEffort(req, effort, withoutReasoningControls)
	}
	return requestparams.CapResponsesTokens(req, ceilings.OutputTokens)
}

// ChatRequestExceedsCeilings reports whether the ceilings would change req.
func ChatRequestExceedsCeilings(req *core.ChatRequest, ceilings requestparams.Ceilings) bool {
	capped, err := ChatRequestWithCeilings(req, ceilings)
	return err != nil || capped != req
}

// effortWithinCeiling reports whether the efforts a request carries, in any
// spelling, have to be replaced, and the effort that replaces them all. One
// above ceiling is replaced by the ceiling, and so is one the common scale
// cannot rank, so no spelling slips past it. A control that is off where
// decoders read it but not in every occurrence (requested.unsettledOff) is
// replaced too, by the highest effort the request does ask for, "none" when
// there is no other: a ceiling never switches reasoning on.
func effortWithinCeiling(reasoning *core.Reasoning, fields core.UnknownJSONFields, ceiling string) (effort string, exceeds bool) {
	if ceiling == "" {
		return "", false
	}
	found := requestedEfforts(reasoning, fields)
	limit, highest := requestparams.EffortRank(ceiling), 0
	for _, level := range found.efforts {
		rank := requestparams.EffortRank(level)
		if rank < 0 || rank > limit {
			return ceiling, true
		}
		highest = max(highest, rank)
	}
	return requestparams.ReasoningEffortLevels()[highest], found.unsettledOff
}
