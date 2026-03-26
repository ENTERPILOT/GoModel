package executionplans

import (
	"fmt"

	"gomodel/internal/core"
	"gomodel/internal/guardrails"
)

type compiler struct {
	registry    *guardrails.Registry
	featureCaps core.ExecutionFeatures
}

// NewCompiler creates the default execution-plan compiler for the v1 payload.
func NewCompiler(registry *guardrails.Registry) Compiler {
	return NewCompilerWithFeatureCaps(registry, core.DefaultExecutionFeatures())
}

// NewCompilerWithFeatureCaps creates the default execution-plan compiler for the
// v1 payload with process-level feature caps applied at compile time.
func NewCompilerWithFeatureCaps(registry *guardrails.Registry, featureCaps core.ExecutionFeatures) Compiler {
	return &compiler{
		registry:    registry,
		featureCaps: featureCaps,
	}
}

func (c *compiler) Compile(version Version) (*CompiledPlan, error) {
	features := core.ExecutionFeatures(version.Payload.Features).ApplyUpperBound(c.featureCaps)
	policy := &core.ResolvedExecutionPolicy{
		VersionID:      version.ID,
		Version:        version.Version,
		ScopeProvider:  version.Scope.Provider,
		ScopeModel:     version.Scope.Model,
		Name:           version.Name,
		PlanHash:       version.PlanHash,
		Features:       features,
		GuardrailsHash: "",
	}

	var pipeline *guardrails.Pipeline
	if policy.Features.Guardrails {
		steps := make([]guardrails.StepReference, 0, len(version.Payload.Guardrails))
		for _, step := range version.Payload.Guardrails {
			steps = append(steps, guardrails.StepReference{
				Ref:  step.Ref,
				Step: step.Step,
			})
		}

		var err error
		pipeline, policy.GuardrailsHash, err = c.compileGuardrails(steps)
		if err != nil {
			return nil, err
		}
	}

	return &CompiledPlan{
		Version:  version,
		Policy:   policy,
		Pipeline: pipeline,
	}, nil
}

func (c *compiler) compileGuardrails(steps []guardrails.StepReference) (*guardrails.Pipeline, string, error) {
	if len(steps) == 0 {
		return nil, "", nil
	}
	if c == nil || c.registry == nil {
		return nil, "", fmt.Errorf("guardrails are enabled but no guardrail registry is configured")
	}
	return c.registry.BuildPipeline(steps)
}
