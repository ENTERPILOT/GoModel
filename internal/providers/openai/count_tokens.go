package openai

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// chatToolPreambleTokens is what Chat Completions bills on top of the
// Responses count for a request with tools on GPT-5 and later: a fixed tools
// preamble (the per-tool framing is the same on both APIs). Measured on
// gpt-5.5, gpt-5.6-terra, gpt-6-sol and gpt-6-luna.
const chatToolPreambleTokens = 82

// CountChatTokens counts a chat request's input tokens with OpenAI's
// /responses/input_tokens endpoint, for the request as the path that will
// serve it sends it: a request served through the Responses API is counted as
// translated, one served through Chat Completions with its chat adaptations
// applied and, when it carries tools on GPT-5 and later, the Chat tools
// preamble added. A request the translation cannot carry exactly answers
// core.ErrMessagesTokenCountUnsupported, so the caller estimates instead.
func (p *Provider) CountChatTokens(ctx context.Context, req *core.ChatRequest) (int, error) {
	// Stop sequences do not affect the input; the translation would refuse
	// the member, so it is dropped first.
	counted := *req
	counted.ExtraFields = req.ExtraFields.Without("stop")
	countedReq := &counted
	_, viaResponses := responsesRoute(countedReq)
	if !viaResponses {
		adapted, err := p.adaptedChatRequest(countedReq)
		if err != nil {
			return 0, core.ErrMessagesTokenCountUnsupported
		}
		countedReq = adapted
	}
	responsesReq, err := chatToResponsesRequest(countedReq)
	if err != nil {
		return 0, core.ErrMessagesTokenCountUnsupported
	}
	// Output and storage settings do not affect the input count.
	responsesReq.MaxOutputTokens = nil
	responsesReq.Store = nil
	resp, err := p.CountResponseInputTokens(ctx, responsesReq)
	if err != nil {
		return 0, err
	}
	count := resp.InputTokens
	if !viaResponses && len(countedReq.Tools) > 0 && isGPT5PlusModel(countedReq.Model) {
		count += chatToolPreambleTokens
	}
	return count, nil
}
