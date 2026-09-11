package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsestore"
)

// PatchResponsesAttempt resolves previous_response_id for an attempt whose
// provider cannot. It runs once per dispatch attempt with that attempt's
// provider type: a native Responses provider keeps the id untouched, since it
// holds that state itself, while a chat-translated provider keeps no response
// state, so the referenced response is loaded from the gateway's response
// store and its input items and output are prepended to the request input,
// the way a gateway-managed conversation is; the field is stripped before
// dispatch. Deciding per attempt keeps a native primary's request unchanged
// and still gives a translated failover target a request it can serve.
func (s *translatedInferenceService) PatchResponsesAttempt(ctx context.Context, req *core.ResponsesRequest, providerType string) (*core.ResponsesRequest, error) {
	if req == nil {
		return req, nil
	}
	id := strings.TrimSpace(req.PreviousResponseID)
	if id == "" || s.providerTypeResolvesPreviousResponse(providerType) {
		return req, nil
	}
	store := s.currentResponseStore()
	if store == nil {
		// No local store: keep the historical behavior, where the provider
		// adapter rejects the field.
		return req, nil
	}

	history, err := s.previousResponseHistory(ctx, store, id)
	if err != nil {
		return req, err
	}
	merged, err := mergeConversationInput(history, req.Input)
	if err != nil {
		return req, err
	}

	patched := *req
	patched.Input = merged
	patched.PreviousResponseID = ""
	return &patched, nil
}

// maxPreviousResponseChain bounds the walk back through stored responses, so
// a corrupted or adversarial chain cannot keep a request busy indefinitely.
const maxPreviousResponseChain = 256

// previousResponseHistory reconstructs the conversation behind a stored
// response: each stored response holds only its own input items and output,
// as OpenAI's input_items do, and links to its own predecessor, so the chain
// is walked back to its root and replayed oldest first. The head must exist
// for the caller; an ancestor that has expired or is out of scope ends the
// walk, leaving the history that is still available.
func (s *translatedInferenceService) previousResponseHistory(ctx context.Context, store responsestore.Store, id string) ([]json.RawMessage, error) {
	var segments [][]json.RawMessage
	seen := make(map[string]struct{})
	for hop := 0; id != ""; hop++ {
		if hop >= maxPreviousResponseChain {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("previous_response_id chain exceeds %d responses", maxPreviousResponseChain), nil)
		}
		if _, cyclic := seen[id]; cyclic {
			break
		}
		seen[id] = struct{}{}

		// The predecessor's snapshot is written in the background; a client
		// that chains as soon as it has the response must not race that write.
		s.awaitPendingSnapshot(ctx, id)
		stored, err := store.Get(ctx, id)
		if err != nil && !errors.Is(err, responsestore.ErrNotFound) {
			return nil, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to load previous response", err)
		}
		// Another tenant's response is indistinguishable from a missing one.
		missing := err != nil || stored == nil || stored.Response == nil || !core.AccessScopeFromContext(ctx).Allows(stored.UserPath)
		if missing {
			if hop == 0 {
				return nil, previousResponseNotFound(id)
			}
			break
		}

		segment := make([]json.RawMessage, 0, len(stored.InputItems)+len(stored.Response.Output))
		segment = append(segment, stored.InputItems...)
		for _, item := range stored.Response.Output {
			item, ok := replayableOutputItem(item)
			if !ok {
				continue
			}
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, core.NewProviderError("response_store", http.StatusInternalServerError, "stored response output is not serializable", err)
			}
			segment = append(segment, raw)
		}
		segments = append(segments, segment)
		id = strings.TrimSpace(stored.Response.PreviousResponseID)
	}

	var history []json.RawMessage
	for _, segment := range slices.Backward(segments) {
		history = append(history, segment...)
	}
	return history, nil
}

// replayableOutputItem drops the output_text parts of a message that carry no
// text, and the message itself when nothing is left: chat translation rejects
// an empty text part, and an empty assistant turn adds nothing to the history.
// Other item types (function calls, reasoning) are replayed unchanged.
func replayableOutputItem(item core.ResponsesOutputItem) (core.ResponsesOutputItem, bool) {
	if item.Type != "message" {
		return item, true
	}
	content := make([]core.ResponsesContentItem, 0, len(item.Content))
	for _, part := range item.Content {
		if part.Type == "output_text" && part.Text == "" {
			continue
		}
		content = append(content, part)
	}
	if len(content) == 0 {
		return item, false
	}
	item.Content = content
	return item, true
}

// providerTypeResolvesPreviousResponse reports whether a provider type
// supports the native Responses lifecycle and so resolves previous_response_id
// itself. Without a provider inventory the field is forwarded as before.
func (s *translatedInferenceService) providerTypeResolvesPreviousResponse(providerType string) bool {
	if providerType == "" {
		return true
	}
	native, err := nativeResponseProviderTypes(s.provider)
	if err != nil {
		return true
	}
	return slices.Contains(native, providerType)
}

func previousResponseNotFound(id string) error {
	return core.NewNotFoundError(fmt.Sprintf("Previous response with id '%s' not found.", id))
}
