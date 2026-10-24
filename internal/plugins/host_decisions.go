package plugins

import (
	"context"
	"errors"
	"strings"

	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/pluginapi"
)

// Decider runs a gateway-internal Decisions request: model resolution,
// failover, usage, and audit apply as on POST /v1/decisions. The body and
// the answer are OpenAI's Decisions wire format.
type Decider interface {
	Decide(ctx context.Context, body []byte) ([]byte, error)
	// DecisionModel reports whether model answers decisions only; see
	// [pluginapi.Decisions.DecisionModel].
	DecisionModel(model string) bool
}

// ErrDecisionsUnavailable is returned when no Decider is wired.
var ErrDecisionsUnavailable = errors.New("plugins: decisions are not available")

var _ pluginapi.DecisionsHost = (*host)(nil)

func (h *host) Decisions() pluginapi.Decisions { return (*hostDecisions)(h) }

// hostDecisions is the host's [pluginapi.Decisions].
type hostDecisions host

// Decide asks a decision model through the gateway with origin "plugin",
// scoped like [host.Complete].
func (d *hostDecisions) Decide(ctx context.Context, req pluginapi.DecisionRequest) (*pluginapi.DecisionResult, error) {
	h := (*host)(d)
	if h.deps.Decisions == nil {
		return nil, ErrDecisionsUnavailable
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		return nil, errors.New("plugins: decision model is required")
	}
	body, err := exchange.DecisionRequestBody(req)
	if err != nil {
		return nil, err
	}
	ctx, err = h.inferenceContext(ctx, req.UserPath)
	if err != nil {
		return nil, err
	}
	answer, err := h.deps.Decisions.Decide(ctx, body)
	if err != nil {
		return nil, err
	}
	return exchange.FromDecisionBody(answer)
}

func (d *hostDecisions) DecisionModel(model string) bool {
	if d.deps.Decisions == nil {
		return false
	}
	return d.deps.Decisions.DecisionModel(strings.TrimSpace(model))
}
