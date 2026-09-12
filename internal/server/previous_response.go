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
	"github.com/enterpilot/gomodel/internal/gateway"
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
//
// Most requests arrive here already expanded (see ResolveResponsesHistory);
// only an id kept for a native primary can still need it. An id forwarded to
// a native provider passes the ownership check every attempt, so a stored
// response outside the caller's access scope is never resolved upstream under
// the gateway's shared provider credential.
func (s *translatedInferenceService) PatchResponsesAttempt(ctx context.Context, req *core.ResponsesRequest, providerType string) (*core.ResponsesRequest, error) {
	if req == nil || strings.TrimSpace(req.PreviousResponseID) == "" {
		return req, nil
	}
	if s.providerTypeResolvesPreviousResponse(providerType) {
		_, err := s.nativePreviousResponse(ctx, req.PreviousResponseID)
		return req, err
	}
	return s.withPreviousResponseHistory(ctx, req)
}

// ResolveResponsesHistory expands the history a Responses request refers to
// before the prompt phase, so guardrails see (and anonymize, block, or
// rewrite) the replayed turns exactly as they would in an input the client
// replayed itself. A stored response holds what its client saw, restored
// values included, so replaying it past the guardrails would hand those
// values to the provider. The client's own turn is kept in the context for
// the response snapshot. previous_response_id is expanded when the primary
// target (providerTypes[0]) is chat-translated; when guardrails run and
// either a failover target is chat-translated or the prompt phase edits
// content; and when the id names a response the gateway minted for a
// chat-translated provider, which a native provider cannot resolve.
// Otherwise a native primary keeps the id, which it resolves itself, and
// PatchResponsesAttempt expands it for a translated failover attempt. A
// native primary also keeps an id the gateway has not stored. An id whose
// stored response belongs to another tenant is reported missing whatever the
// target resolves it.
func (s *translatedInferenceService) ResolveResponsesHistory(ctx context.Context, req *core.ResponsesRequest, providerTypes []string) (context.Context, *core.ResponsesRequest, error) {
	if req == nil {
		return ctx, req, nil
	}
	ctx = context.WithValue(ctx, clientTurnKey{}, &clientTurn{previousResponseID: req.PreviousResponseID, input: req.Input})
	ctx, req, err := s.applyResponsesConversation(ctx, req)
	if err != nil || strings.TrimSpace(req.PreviousResponseID) == "" {
		return ctx, req, err
	}
	translated := func(providerType string) bool { return !s.providerTypeResolvesPreviousResponse(providerType) }
	primaryTranslated := len(providerTypes) > 0 && translated(providerTypes[0])
	guarded := s.promptPhaseRuns(ctx) &&
		(slices.ContainsFunc(providerTypes, translated) || s.promptPhaseEditsContent(ctx))
	if !primaryTranslated {
		// A chat-translated primary loads the stored response below, which
		// enforces the same ownership rule; a native one has to be told.
		replay, err := s.nativePreviousResponse(ctx, req.PreviousResponseID)
		if err != nil {
			return ctx, req, err
		}
		if !guarded && !replay {
			return ctx, req, nil
		}
	}
	patched, err := s.withPreviousResponseHistory(ctx, req)
	if err != nil && !primaryTranslated {
		if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok && gatewayErr.Type == core.ErrorTypeNotFound {
			return ctx, req, nil
		}
	}
	return ctx, patched, err
}

// promptPhaseRuns reports whether a prompt-phase patch runs for the request.
func (s *translatedInferenceService) promptPhaseRuns(ctx context.Context) bool {
	return s.translatedRequestPatcher != nil && core.GetWorkflow(ctx).GuardrailsEnabled()
}

// promptPhaseEditsContent reports whether the prompt phase may rewrite the
// request's content. Only a rewriting prompt phase needs the replayed history
// in the input: an anonymizing guardrail that never sees the earlier turns
// reuses their placeholders for new values, so the restore step hands the
// client another turn's data. A patcher without the capability is assumed to
// rewrite.
func (s *translatedInferenceService) promptPhaseEditsContent(ctx context.Context) bool {
	editor, ok := s.translatedRequestPatcher.(gateway.PromptContentEditor)
	return !ok || editor.EditsPromptContent(ctx)
}

// nativePreviousResponse inspects the stored response behind an id that would
// otherwise be forwarded to a native Responses provider, which resolves it
// upstream under the gateway's shared provider credential. It reports whether
// the gateway has to replay the history itself: a response the gateway minted
// while serving a chat-translated provider exists only in its store, so a
// native provider rejects the id. A stored response outside the caller's
// access scope is reported missing, exactly as GET /v1/responses/{id} reports
// it, so one tenant cannot continue (and read back) another tenant's
// conversation. An id the gateway does not hold — a response created directly
// against the provider, or one that was not stored — is still forwarded
// untouched.
func (s *translatedInferenceService) nativePreviousResponse(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	store := s.currentResponseStore()
	if id == "" || store == nil {
		return false, nil
	}
	// A client that chains as soon as it has the response must not race the
	// background snapshot write.
	s.awaitPendingSnapshot(ctx, id)
	stored, err := store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, responsestore.ErrNotFound) {
			return false, nil
		}
		return false, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to load previous response", err)
	}
	if stored == nil || stored.Response == nil {
		return false, nil
	}
	if !core.AccessScopeFromContext(ctx).Allows(stored.UserPath) {
		return false, previousResponseNotFound(id)
	}
	return !s.providerTypeResolvesPreviousResponse(stored.Provider), nil
}

// clientTurn is a Responses request's own turn as the client sent it, before
// history expansion and the prompt phase: the snapshot stores its input
// items, as OpenAI's input_items do, and names its predecessor.
type clientTurn struct {
	previousResponseID string
	input              any
}

type clientTurnKey struct{}

// clientInput returns the request whose input the snapshot of req stores:
// the client's own turn when it was recorded, req itself otherwise.
func clientInput(ctx context.Context, req *core.ResponsesRequest) *core.ResponsesRequest {
	if turn, ok := ctx.Value(clientTurnKey{}).(*clientTurn); ok {
		return &core.ResponsesRequest{Input: turn.input}
	}
	return req
}

// chainedFrom returns the previous_response_id the client sent, which
// expansion clears from req.
func chainedFrom(ctx context.Context, req *core.ResponsesRequest) string {
	if turn, ok := ctx.Value(clientTurnKey{}).(*clientTurn); ok {
		return turn.previousResponseID
	}
	if req == nil {
		return ""
	}
	return req.PreviousResponseID
}

// withPreviousResponseHistory prepends the stored history behind
// req.PreviousResponseID to its input and clears the field.
func (s *translatedInferenceService) withPreviousResponseHistory(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	id := strings.TrimSpace(req.PreviousResponseID)
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
