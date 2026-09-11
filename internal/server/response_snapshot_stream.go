package server

import (
	"bytes"
	"context"
	"io"
	"slices"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// snapshotStream stores a streamed response the way a buffered one is
// stored: the terminal event carries the complete response, and its snapshot
// is written in the background once that event passes through. The bytes
// sent to the client are untouched.
func (s *translatedInferenceService) snapshotStream(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest, providerType, providerName, requestID string, stream io.ReadCloser) io.ReadCloser {
	if s.currentResponseStore() == nil || (req.Store != nil && !*req.Store) {
		return stream
	}
	return streaming.NewObservedSSEStream(stream, &responseSnapshotStreamObserver{
		service:      s,
		ctx:          ctx,
		workflow:     workflow,
		req:          req,
		providerType: providerType,
		providerName: providerName,
		requestID:    requestID,
	})
}

type responseSnapshotStreamObserver struct {
	service      *translatedInferenceService
	ctx          context.Context
	workflow     *core.Workflow
	req          *core.ResponsesRequest
	providerType string
	providerName string
	requestID    string
	stored       bool
}

// responsesTerminalEvents are the events that carry the finished response.
// A truncated or failed turn is stored like its buffered counterpart, whose
// status the client also sees, so it can be retrieved and chained on.
var responsesTerminalEvents = []string{"response.completed", "response.incomplete", "response.failed", "response.done"}

func (o *responseSnapshotStreamObserver) WantsJSONEvent(raw []byte) bool {
	if o.stored {
		return false
	}
	for _, event := range responsesTerminalEvents {
		if bytes.Contains(raw, []byte(event)) {
			return true
		}
	}
	return false
}

func (o *responseSnapshotStreamObserver) OnJSONEvent(payload map[string]any) {
	if o.stored {
		return
	}
	if eventType, _ := payload["type"].(string); !slices.Contains(responsesTerminalEvents, eventType) {
		return
	}
	body, ok := payload["response"].(map[string]any)
	if !ok {
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	var resp core.ResponsesResponse
	if err := json.Unmarshal(raw, &resp); err != nil || resp.ID == "" {
		return
	}
	o.stored = true
	resp.PreviousResponseID = chainedFrom(o.ctx, o.req)
	o.service.storeResponseSnapshotAsync(o.ctx, o.workflow, o.req, &resp, o.providerType, o.providerName, o.requestID)
}

func (o *responseSnapshotStreamObserver) OnStreamClose() {}
