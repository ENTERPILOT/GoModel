package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"

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
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&item); err != nil {
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

// appendExchange records the completed turn and returns the output items with
// their final persisted IDs. The caller can therefore expose exactly the IDs a
// later conversation item retrieve or delete request can address.
func (t *conversationTurn) appendExchange(ctx context.Context, responseID string, output []json.RawMessage) ([]json.RawMessage, error) {
	items := normalizedResponseInputItems(responseID, &core.ResponsesRequest{Input: t.input})
	inputCount := len(items)
	items = append(items, output...)
	if len(items) == 0 {
		return nil, nil
	}
	items = normalizeConversationItemIDs(nil, items)
	err := t.store.AppendItems(ctx, t.id, items)
	for range 3 {
		if !errors.Is(err, conversationstore.ErrDuplicateItem) {
			break
		}
		stored, getErr := t.store.Get(ctx, t.id)
		if getErr != nil {
			err = getErr
			break
		}
		items = normalizeConversationItemIDs(stored.Items, items)
		err = t.store.AppendItems(ctx, t.id, items)
	}
	if err != nil {
		return nil, err
	}
	return items[inputCount:], nil
}

func normalizeConversationItemIDs(existing, added []json.RawMessage) []json.RawMessage {
	ids := make(map[string]struct{}, len(existing)+len(added))
	for _, raw := range existing {
		if id := responseInputItemID(raw); id != "" {
			ids[id] = struct{}{}
		}
	}
	result := make([]json.RawMessage, 0, len(added))
	for _, raw := range added {
		item, err := decodeRawJSONObject(raw)
		if err != nil {
			result = append(result, core.CloneRawJSON(raw))
			continue
		}
		id := rawJSONString(item, "id")
		if _, duplicate := ids[id]; id == "" || duplicate {
			id = generatedConversationItemID(rawJSONString(item, "type"))
			_ = setRawJSONValue(item, "id", id)
		}
		ids[id] = struct{}{}
		encoded, err := json.Marshal(item)
		if err != nil {
			result = append(result, core.CloneRawJSON(raw))
			continue
		}
		result = append(result, encoded)
	}
	return result
}

// appendResponse records a completed non-streaming response and updates its
// output IDs to the IDs committed to the conversation store.
func (t *conversationTurn) appendResponse(ctx context.Context, resp *core.ResponsesResponse) error {
	if resp == nil {
		return nil
	}
	output := make([]json.RawMessage, 0, len(resp.Output))
	for _, item := range resp.Output {
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal conversation output: %w", err)
		}
		output = append(output, raw)
	}
	persistedOutput, err := t.appendExchange(ctx, resp.ID, output)
	if err != nil {
		return err
	}
	for index := range min(len(resp.Output), len(persistedOutput)) {
		resp.Output[index].ID = responseInputItemID(persistedOutput[index])
	}
	return nil
}
