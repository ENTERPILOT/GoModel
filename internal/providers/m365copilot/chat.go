package m365copilot

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// conversation is the copilotConversation resource, which the chat action
// returns whole and the chatOverStream action returns once per event.
// https://learn.microsoft.com/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotconversation
type conversation struct {
	ID       string                `json:"id"`
	State    string                `json:"state"`
	Messages []conversationMessage `json:"messages"`
}

type conversationMessage struct {
	Text string `json:"text"`
}

// stateDisengagedForRAI is the conversation state after Copilot's
// responsible-AI checks ended the conversation.
const stateDisengagedForRAI = "disengagedForRai"

// maxConversationBytes bounds the body read when creating a conversation,
// which is a few hundred bytes of JSON.
const maxConversationBytes = 1 << 20

// answer returns Copilot's reply in c. Messages carry no role: the service
// echoes the prompt as one message and puts its reply after it, so the reply
// is the last message that is not the echo. A reply that repeats the prompt
// word for word is still the last of several messages.
func (c *conversation) answer(prompt string) (string, bool) {
	prompt = strings.TrimSpace(prompt)
	for _, message := range slices.Backward(c.Messages) {
		if text := message.Text; strings.TrimSpace(text) != prompt {
			return text, true
		}
	}
	if len(c.Messages) > 1 {
		return c.Messages[len(c.Messages)-1].Text, true
	}
	return "", false
}

// finishReason maps how the conversation ended onto an OpenAI finish reason.
func finishReason(disengaged bool) string {
	if disengaged {
		return "content_filter"
	}
	return "stop"
}

// ChatCompletion answers a chat request with one Copilot turn in a new
// conversation.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	turn, err := p.openTurn(ctx, req)
	if err != nil {
		return nil, err
	}
	var conv conversation
	if err := p.client.Do(ctx, turn.request("/chat", req.Model, false), &conv); err != nil {
		return nil, err
	}
	disengaged := conv.State == stateDisengagedForRAI
	text, ok := conv.answer(turn.body.Message.Text)
	if !ok && !disengaged {
		return nil, core.NewNoChoicesProviderError(p.providerName)
	}
	return &core.ChatResponse{
		ID:       completionID(turn.conversationID),
		Object:   "chat.completion",
		Model:    req.Model,
		Provider: p.providerName,
		Created:  time.Now().Unix(),
		Choices: []core.Choice{{
			Message:      core.ResponseMessage{Role: "assistant", Content: text},
			FinishReason: finishReason(disengaged),
		}},
	}, nil
}

// StreamChatCompletion is ChatCompletion for streaming requests: Copilot's
// stream of conversation snapshots becomes chat completion chunks.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	turn, err := p.openTurn(ctx, req)
	if err != nil {
		return nil, err
	}
	body, err := p.client.DoStream(ctx, turn.request("/chatOverStream", req.Model, true))
	if err != nil {
		return nil, err
	}
	return newChatStream(body, &streamState{
		id:           completionID(turn.conversationID),
		model:        req.Model,
		provider:     p.providerName,
		prompt:       turn.body.Message.Text,
		includeUsage: req.StreamOptions != nil && req.StreamOptions.IncludeUsage,
		created:      time.Now().Unix(),
	}), nil
}

// turn is one prompt ready to send into a conversation created for it.
type turn struct {
	conversationID string
	body           *chatBody
	headers        http.Header
}

// openTurn translates the request and creates its conversation. The request
// is validated first, so a request Copilot cannot serve costs no upstream
// call.
func (p *Provider) openTurn(ctx context.Context, req *core.ChatRequest) (*turn, error) {
	body, err := newChatBody(req, p.timeZone)
	if err != nil {
		return nil, err
	}
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return nil, err
	}
	id, err := p.createConversation(ctx, headers)
	if err != nil {
		return nil, err
	}
	return &turn{conversationID: id, body: body, headers: headers}, nil
}

// request builds the call that sends the prompt to action, "/chat" or
// "/chatOverStream".
func (t *turn) request(action, model string, stream bool) llmclient.Request {
	return llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/conversations/" + url.PathEscape(t.conversationID) + action,
		Operation: llmclient.OperationChat,
		Model:     model,
		Stream:    stream,
		Body:      t.body,
		Headers:   t.headers,
	}
}

// createConversation opens an empty Copilot conversation and returns its ID.
// The call answers 201, which the client's typed calls treat as a failure, so
// it goes through the passthrough path and reads the status itself.
func (p *Provider) createConversation(ctx context.Context, headers http.Header) (string, error) {
	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/conversations",
		Body:     struct{}{},
		Headers:  headers,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConversationBytes))
	if err != nil {
		return "", core.NewProviderError(p.providerName, http.StatusBadGateway, "failed to read the Microsoft 365 Copilot conversation", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", core.ParseProviderError(p.providerName, resp.StatusCode, body, nil)
	}
	var conv conversation
	if err := json.Unmarshal(body, &conv); err != nil || strings.TrimSpace(conv.ID) == "" {
		return "", core.NewProviderError(p.providerName, http.StatusBadGateway, "Microsoft 365 Copilot returned no conversation ID", err)
	}
	return conv.ID, nil
}

// completionID derives the chat completion ID from the Copilot conversation.
func completionID(conversationID string) string {
	return "chatcmpl-" + conversationID
}
