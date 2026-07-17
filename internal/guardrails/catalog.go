package guardrails

import "github.com/enterpilot/gomodel/internal/core"

// Catalog resolves named guardrail references into executable pipelines.
// BuildPipeline returns the compiled pipeline, a deterministic configuration hash
// for cache/change detection, and an error. The hash should change whenever the
// effective pipeline configuration changes; empty is reserved for "no pipeline".
type Catalog interface {
	Len() int
	Names() []string
	GuardrailNames() []string
	HeaderPolicyNames() []string
	BuildPipeline(steps []StepReference) (*Pipeline, string, error)
	BuildHeaderPolicies(steps []StepReference) ([]core.HeaderPolicy, string, error)
}
