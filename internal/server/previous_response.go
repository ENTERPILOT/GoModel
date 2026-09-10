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

// applyResponsesPreviousResponse resolves previous_response_id for routes
// whose provider cannot. A chat-translated provider keeps no response state,
// so the referenced response is loaded from the gateway's response store and
// its input items and output are prepended to the request input, the way a
// gateway-managed conversation is; the field is stripped before dispatch.
// Native Responses providers keep receiving the id untouched, since they hold
// that state themselves. A stored response's input items already carry the
// history it was chained from, so one hop reconstructs the whole chain.
func (s *translatedInferenceService) applyResponsesPreviousResponse(ctx context.Context, req *core.ResponsesRequest, workflow *core.Workflow) (*core.ResponsesRequest, error) {
	if req == nil {
		return req, nil
	}
	id := strings.TrimSpace(req.PreviousResponseID)
	if id == "" || s.providerResolvesPreviousResponse(workflow) {
		return req, nil
	}
	store := s.currentResponseStore()
	if store == nil {
		// No local store: keep the historical behavior, where the provider
		// adapter rejects the field.
		return req, nil
	}

	stored, err := store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, responsestore.ErrNotFound) {
			return req, previousResponseNotFound(id)
		}
		return req, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to load previous response", err)
	}
	// Another tenant's response is indistinguishable from a missing one.
	if stored == nil || stored.Response == nil || !core.AccessScopeFromContext(ctx).Allows(stored.UserPath) {
		return req, previousResponseNotFound(id)
	}

	history := make([]json.RawMessage, 0, len(stored.InputItems)+len(stored.Response.Output))
	history = append(history, stored.InputItems...)
	for _, item := range stored.Response.Output {
		raw, err := json.Marshal(item)
		if err != nil {
			return req, core.NewProviderError("response_store", http.StatusInternalServerError, "stored response output is not serializable", err)
		}
		history = append(history, raw)
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

// providerResolvesPreviousResponse reports whether the resolved route's
// provider supports the native Responses lifecycle and so resolves
// previous_response_id itself. Without a provider inventory the field is
// forwarded as before.
func (s *translatedInferenceService) providerResolvesPreviousResponse(workflow *core.Workflow) bool {
	if workflow == nil || workflow.ProviderType == "" {
		return true
	}
	native, err := nativeResponseProviderTypes(s.provider)
	if err != nil {
		return true
	}
	return slices.Contains(native, workflow.ProviderType)
}

func previousResponseNotFound(id string) error {
	return core.NewNotFoundError(fmt.Sprintf("Previous response with id '%s' not found.", id))
}
