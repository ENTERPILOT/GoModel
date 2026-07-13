package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
)

// Gateway-managed conversations live in the local conversation store; upstream
// providers have never seen their IDs. A /v1/responses call that references one
// is therefore resolved locally: the stored history is prepended to the request
// input, the conversation field is stripped before dispatch, and the new
// exchange (user input + model output) is appended to the store afterwards.
// This works uniformly for native Responses providers and chat-translated ones.

// conversationTurnKey carries the in-flight conversation turn through the
// prepared request context so dispatch can persist the exchange on completion.
type conversationTurnKey struct{}

// conversationTurn tracks one /v1/responses call bound to a gateway-managed
// conversation: where to append and the request's own (pre-merge) input that
// becomes the user side of the turn.
type conversationTurn struct {
	store conversationstore.Store
	id    string
	input any
}

func conversationTurnFromContext(ctx context.Context) *conversationTurn {
	turn, _ := ctx.Value(conversationTurnKey{}).(*conversationTurn)
	return turn
}

// applyResponsesConversation resolves a gateway-managed conversation reference
// on a prepared request. Requests without one pass through untouched. A
// missing conversation fails with the same 404 contract OpenAI uses.
func (s *translatedInferenceService) applyResponsesConversation(ctx context.Context, req *core.ResponsesRequest) (context.Context, *core.ResponsesRequest, error) {
	if req == nil || req.Conversation == nil {
		return ctx, req, nil
	}
	store := s.currentConversationStore()
	if store == nil {
		// No local store configured: keep the historical pass-through behavior.
		return ctx, req, nil
	}
	if req.PreviousResponseID != "" {
		return ctx, req, core.NewInvalidRequestError("previous_response_id cannot be used together with conversation", nil)
	}
	id := req.Conversation.ID
	if id == "" {
		return ctx, req, core.NewInvalidRequestError("conversation id is required", nil)
	}

	stored, err := store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, conversationstore.ErrNotFound) {
			return ctx, req, core.NewNotFoundError(fmt.Sprintf("Conversation with id '%s' not found.", id))
		}
		return ctx, req, core.NewProviderError("conversation_store", 500, "failed to load conversation", err)
	}

	merged, err := mergeConversationInput(stored.Items, req.Input)
	if err != nil {
		return ctx, req, err
	}

	patched := *req
	patched.Input = merged
	patched.Conversation = nil

	turn := &conversationTurn{store: store, id: id, input: req.Input}
	return context.WithValue(ctx, conversationTurnKey{}, turn), &patched, nil
}

// mergeConversationInput prepends stored history items to the request input,
// producing the []any union shape every downstream path already accepts.
// Stored item IDs are gateway-generated and stripped so upstream providers do
// not treat them as item references.
func mergeConversationInput(history []json.RawMessage, input any) ([]any, error) {
	merged := make([]any, 0, len(history))
	for _, raw := range history {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, core.NewProviderError("conversation_store", 500, "stored conversation item is not valid JSON", err)
		}
		delete(item, "id")
		merged = append(merged, item)
	}

	switch in := input.(type) {
	case nil:
		return merged, nil
	case string:
		return append(merged, map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": in},
			},
		}), nil
	case []core.ResponsesInputElement:
		for _, item := range in {
			merged = append(merged, item)
		}
		return merged, nil
	case []map[string]any:
		for _, item := range in {
			merged = append(merged, item)
		}
		return merged, nil
	case []any:
		return append(merged, in...), nil
	default:
		return nil, core.NewInvalidRequestError("input must be a string or an array of input items", nil)
	}
}

// appendExchange records the completed turn: the request input normalized as
// stored input items, followed by the response output. Failures only log —
// the client already has its response, so the turn must not fail after the fact.
func (t *conversationTurn) appendExchange(ctx context.Context, responseID string, output []json.RawMessage) {
	items := normalizedResponseInputItems(responseID, &core.ResponsesRequest{Input: t.input})
	items = append(items, output...)
	if len(items) == 0 {
		return
	}
	if err := t.store.AppendItems(ctx, t.id, items); err != nil {
		slog.Warn("conversation append failed", "conversation_id", t.id, "error", err)
	}
}

// appendResponse records a completed non-streaming response on the turn.
func (t *conversationTurn) appendResponse(ctx context.Context, resp *core.ResponsesResponse) {
	if resp == nil {
		return
	}
	output := make([]json.RawMessage, 0, len(resp.Output))
	for _, item := range resp.Output {
		raw, err := json.Marshal(item)
		if err != nil {
			slog.Warn("conversation append failed: marshal output", "conversation_id", t.id, "error", err)
			return
		}
		output = append(output, raw)
	}
	t.appendExchange(ctx, resp.ID, output)
}

// streamObserver returns a streaming.Observer that captures the final
// response.completed event and appends the exchange when the stream closes.
// The context is detached from the request so the append survives the client
// connection ending right after the final event.
func (t *conversationTurn) streamObserver(ctx context.Context) *conversationStreamObserver {
	return &conversationStreamObserver{turn: t, ctx: context.WithoutCancel(ctx)}
}

type conversationStreamObserver struct {
	turn     *conversationTurn
	ctx      context.Context
	response map[string]any
}

func (o *conversationStreamObserver) OnJSONEvent(payload map[string]any) {
	eventType, _ := payload["type"].(string)
	if eventType != "response.completed" && eventType != "response.done" {
		return
	}
	if response, ok := payload["response"].(map[string]any); ok {
		o.response = response
	}
}

func (o *conversationStreamObserver) OnStreamClose() {
	if o.response == nil {
		return
	}
	responseID, _ := o.response["id"].(string)
	outputItems, _ := o.response["output"].([]any)
	output := make([]json.RawMessage, 0, len(outputItems))
	for _, item := range outputItems {
		raw, err := json.Marshal(item)
		if err != nil {
			slog.Warn("conversation append failed: marshal streamed output", "conversation_id", o.turn.id, "error", err)
			return
		}
		output = append(output, raw)
	}
	o.turn.appendExchange(o.ctx, responseID, output)
}
