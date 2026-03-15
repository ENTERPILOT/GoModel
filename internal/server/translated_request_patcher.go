package server

import (
	"context"

	"gomodel/internal/core"
)

// TranslatedRequestPatcher applies request-level transforms for translated
// routes after planning has resolved the concrete execution selector.
type TranslatedRequestPatcher interface {
	PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error)
	PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error)
}
