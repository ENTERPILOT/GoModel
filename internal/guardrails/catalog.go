package guardrails

import (
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// StepReference points a workflow step at one named guardrail instance in one
// phase. An empty Phase means prompt.
type StepReference struct {
	Ref   string
	Phase pluginapi.Kind
	Step  int
}

// Catalog resolves named guardrail references into per-phase plugin chains.
// BuildChains returns nil when steps is empty; the chain hashes change
// whenever the effective configuration changes.
type Catalog interface {
	Len() int
	Names() []string
	BuildChains(steps []StepReference) (*plugins.Chains, error)
}
