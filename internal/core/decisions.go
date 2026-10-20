package core

import (
	"context"

	"github.com/goccy/go-json"
)

// DecisionRequest is the part of an OpenAI Decisions request the gateway
// reads: the model it routes on and the input guardrails inspect. The
// questions and every other member reach the provider unchanged.
type DecisionRequest struct {
	Model string `json:"model"`
	// Input is the text or user messages the questions are asked about, as
	// sent: a string, or messages of input_text and input_image parts.
	Input json.RawMessage `json:"input,omitempty"`
}

// DecisionsProvider answers OpenAI Decisions requests (POST /v1/decisions).
// The request and response bodies are OpenAI's wire format: a provider whose
// upstream has another decision API translates both, so callers always see
// OpenAI's shapes.
type DecisionsProvider interface {
	Decisions(ctx context.Context, req *PassthroughRequest) (*PassthroughResponse, error)
}

// RoutableDecisions routes a Decisions request by provider type, preferring
// req.ProviderName when set, and lists the provider types that serve it.
type RoutableDecisions interface {
	Decisions(ctx context.Context, providerType string, req *PassthroughRequest) (*PassthroughResponse, error)
	DecisionsProviderTypes() []string
}
