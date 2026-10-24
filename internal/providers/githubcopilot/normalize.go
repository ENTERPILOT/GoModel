package githubcopilot

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// paddingField is a random-length filler Copilot adds to each chat message so
// response sizes do not reveal content length. It means nothing to clients.
const paddingField = "padding"

// chatCompletion sends a chat request to /chat/completions and normalizes
// the reply: Copilot omits the object type and pads each message.
func (p *Provider) chatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	resp, err := p.ChatCompatible.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	for i := range resp.Choices {
		msg := &resp.Choices[i].Message
		msg.ExtraFields = msg.ExtraFields.Without(paddingField)
	}
	return resp, nil
}

// Embeddings sends the request to /embeddings. Copilot rejects a bare string
// input with a 400, so it is wrapped into the one-element list OpenAI treats
// it as, and the reply gets the list object type Copilot omits.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if req != nil {
		if input, ok := req.Input.(string); ok {
			wrapped := *req
			wrapped.Input = []string{input}
			req = &wrapped
		}
	}
	resp, err := p.ChatCompatible.Embeddings(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "list"
	}
	return resp, nil
}
