// Package guardrails provides a pluggable pipeline for request-level guardrails.
//
// Guardrails intercept requests before they reach providers, allowing
// validation, modification, or rejection.
//
// Guardrails operate on a normalized []Message DTO, decoupled from concrete
// API request types (ChatRequest, ResponsesRequest, etc.). Adapters in the
// GuardedProvider convert between concrete requests and the message list.
//
// Execution is driven by a per-guardrail "order" value:
//   - Guardrails with the same order run in parallel (concurrently).
//   - Groups are executed sequentially in ascending order.
//   - Each group receives the output of the previous group.
//
// Example with orders 0, 0, 1, 2, 2:
//
//	Group 0  ──┬── guardrail A ──┬──▶ Group 1 ── guardrail C ──▶ Group 2 ──┬── guardrail D ──┬──▶ done
//	           └── guardrail B ──┘                                         └── guardrail E ──┘
package guardrails

import (
	"context"

	"gomodel/internal/core"
)

// Message represents a single message in a conversation.
// This is the normalized DTO that all text guardrails operate on,
// decoupled from concrete API request types.
type Message struct {
	Role        string // "system", "user", "assistant", "tool"
	Content     string
	ToolCalls   []core.ToolCall
	ToolCallID  string
	ContentNull bool
}

// Guardrail processes a message list and returns the (possibly modified) messages or an error.
// Returning an error rejects the request before it reaches the provider.
type Guardrail interface {
	// Name returns a human-readable identifier for this guardrail.
	Name() string

	// Process processes a normalized message list.
	// Return the (possibly modified) messages, or an error to reject the request.
	Process(ctx context.Context, msgs []Message) ([]Message, error)
}
