package openai

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// CountChatTokens counts a chat request's input tokens exactly with OpenAI's
// /responses/input_tokens endpoint, using the same translation as requests
// served through the Responses API. A request that translation cannot carry
// exactly answers core.ErrMessagesTokenCountUnsupported, so the caller
// estimates instead.
func (p *Provider) CountChatTokens(ctx context.Context, req *core.ChatRequest) (int, error) {
	// Stop sequences do not affect the input; the translation would refuse
	// the member, so it is dropped first.
	counted := *req
	counted.ExtraFields = req.ExtraFields.Without("stop")
	responsesReq, err := chatToResponsesRequest(&counted)
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
	return resp.InputTokens, nil
}
