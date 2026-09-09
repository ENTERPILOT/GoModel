package anthropic

import (
	"context"
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// countTokensFields are the request members /v1/messages/count_tokens
// accepts. Anthropic rejects unknown members rather than ignoring them, so
// everything else in a Messages request (max_tokens, stream, sampling,
// metadata) is left out.
var countTokensFields = []string{"messages", "model", "system", "tools", "tool_choice", "thinking", "cache_control", "output_config"}

// CountMessagesTokens counts the input tokens of a Messages request with
// Anthropic's own endpoint, which is exact where the gateway's estimate is
// not. body is the client's request as received; only the accepted members
// go upstream, with model replaced by the resolved one.
func (p *Provider) CountMessagesTokens(ctx context.Context, model string, body []byte) (int, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return 0, core.NewInvalidRequestError("invalid messages request: "+err.Error(), err)
	}
	forward := make(map[string]json.RawMessage, len(countTokensFields))
	for _, name := range countTokensFields {
		if raw, ok := fields[name]; ok && !core.IsJSONNull(raw) {
			forward[name] = raw
		}
	}
	encodedModel, err := json.Marshal(model)
	if err != nil {
		return 0, err
	}
	forward["model"] = encodedModel

	var resp struct {
		InputTokens int `json:"input_tokens"`
	}
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages/count_tokens",
		Model:    model,
		Body:     forward,
	}, &resp)
	if err != nil {
		return 0, err
	}
	return resp.InputTokens, nil
}
