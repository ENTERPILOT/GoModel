package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsestore"
)

// applyResponsesPreviousResponse resolves previous_response_id for routes
// whose provider cannot. A chat-translated provider keeps no response state,
// so the referenced response is loaded from the gateway's response store and
// its input items and output are prepended to the request input, the way a
// gateway-managed conversation is; the field is stripped before dispatch. A
// stored response's input items already carry the history it was chained
// from, so one hop reconstructs the whole chain.
//
// A route whose primary and failover targets all support the native
// Responses lifecycle keeps the id untouched, since those providers hold that
// state themselves. When any target is chat-translated the history is
// resolved up front, so a failover attempt never receives an id it cannot use.
func (s *translatedInferenceService) applyResponsesPreviousResponse(ctx context.Context, req *core.ResponsesRequest, workflow *core.Workflow) (*core.ResponsesRequest, error) {
	if req == nil {
		return req, nil
	}
	id := strings.TrimSpace(req.PreviousResponseID)
	if id == "" {
		return req, nil
	}
	primaryNative := s.providerTypeResolvesPreviousResponse(workflow.ProviderType)
	if primaryNative && !s.anyFailoverTargetTranslated(workflow) {
		return req, nil
	}
	store := s.currentResponseStore()
	if store == nil {
		// No local store: keep the historical behavior, where the provider
		// adapter rejects the field.
		return req, nil
	}

	// The predecessor's snapshot is written in the background; a client that
	// chains as soon as it has the response must not race that write.
	s.awaitPendingSnapshot(ctx, pendingSnapshotKey(ctx, id))
	stored, err := store.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, responsestore.ErrNotFound) {
			return req, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to load previous response", err)
		}
		if primaryNative {
			// Not tracked by the gateway; the native provider may still know it.
			return req, nil
		}
		return req, previousResponseNotFound(id)
	}
	// Another tenant's response is indistinguishable from a missing one.
	if stored == nil || stored.Response == nil || !core.AccessScopeFromContext(ctx).Allows(stored.UserPath) {
		return req, previousResponseNotFound(id)
	}

	history := make([]json.RawMessage, 0, len(stored.InputItems)+len(stored.Response.Output))
	history = append(history, stored.InputItems...)
	for _, item := range stored.Response.Output {
		item, ok := replayableOutputItem(item)
		if !ok {
			continue
		}
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

// anyFailoverTargetTranslated reports whether a failover attempt could land
// on a provider without the native Responses lifecycle.
func (s *translatedInferenceService) anyFailoverTargetTranslated(workflow *core.Workflow) bool {
	if s.orchestrator == nil {
		return false
	}
	for _, selector := range s.orchestrator.FailoverSelectors(workflow) {
		if !s.providerTypeResolvesPreviousResponse(s.provider.GetProviderType(selector.QualifiedModel())) {
			return true
		}
	}
	return false
}

func previousResponseNotFound(id string) error {
	return core.NewNotFoundError(fmt.Sprintf("Previous response with id '%s' not found.", id))
}

// pendingSnapshotKey scopes an in-flight snapshot write to the user path it
// was written under, so a caller can only wait for its own tenant's writes
// and learns nothing about another tenant's in-flight response ids.
func pendingSnapshotKey(ctx context.Context, id string) string {
	return core.UserPathFromContext(ctx) + "\x00" + id
}

// trackPendingSnapshot registers an in-flight snapshot write under key, so a
// request chained on that response can wait for it.
func (s *translatedInferenceService) trackPendingSnapshot(key string) chan struct{} {
	done := make(chan struct{})
	s.pendingSnapshotMu.Lock()
	if s.pendingSnapshots == nil {
		s.pendingSnapshots = make(map[string]chan struct{})
	}
	s.pendingSnapshots[key] = done
	s.pendingSnapshotMu.Unlock()
	return done
}

// finishPendingSnapshot releases the waiters of one snapshot write.
func (s *translatedInferenceService) finishPendingSnapshot(key string, done chan struct{}) {
	s.pendingSnapshotMu.Lock()
	if s.pendingSnapshots[key] == done {
		delete(s.pendingSnapshots, key)
	}
	s.pendingSnapshotMu.Unlock()
	close(done)
}

// awaitPendingSnapshot blocks until the snapshot write under key, if one is
// in flight, has finished; it gives up with the request or after the write's
// own timeout, in which case the store lookup decides.
func (s *translatedInferenceService) awaitPendingSnapshot(ctx context.Context, key string) {
	s.pendingSnapshotMu.Lock()
	done := s.pendingSnapshots[key]
	s.pendingSnapshotMu.Unlock()
	if done == nil {
		return
	}
	timer := time.NewTimer(snapshotWriteTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	case <-timer.C:
	}
}
