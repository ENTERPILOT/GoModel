package workflows

import (
	"errors"
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/guardrails"
)

type compiler struct {
	registry    guardrails.Catalog
	featureCaps core.WorkflowFeatures
}

// NewCompiler creates the default workflow compiler for the v1 payload.
func NewCompiler(registry guardrails.Catalog) Compiler {
	return NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures())
}

// NewCompilerWithFeatureCaps creates the default workflow compiler for the
// v1 payload with process-level feature caps applied at compile time.
func NewCompilerWithFeatureCaps(registry guardrails.Catalog, featureCaps core.WorkflowFeatures) Compiler {
	return &compiler{
		registry:    registry,
		featureCaps: featureCaps,
	}
}

func (c *compiler) Compile(version Version) (*CompiledWorkflow, error) {
	features := version.Payload.Features.runtimeFeatures().ApplyUpperBound(c.featureCaps)
	policy := &core.ResolvedWorkflowPolicy{
		VersionID:      version.ID,
		Version:        version.Version,
		ScopeProvider:  version.Scope.Provider,
		ScopeModel:     version.Scope.Model,
		ScopeUserPath:  version.Scope.UserPath,
		Name:           version.Name,
		WorkflowHash:   version.WorkflowHash,
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

	return &CompiledWorkflow{
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
		return nil, "", core.NewProviderError("", http.StatusBadGateway, "guardrails are enabled but no guardrail registry is configured", nil)
	}
	if c.registry.Len() == 0 {
		return nil, "", core.NewProviderError("", http.StatusBadGateway, "guardrails are enabled but no guardrails are loaded", nil)
	}
	pipeline, hash, err := c.registry.BuildPipeline(steps)
	if err == nil {
		return pipeline, hash, nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return nil, "", gatewayErr
	}
	return nil, "", core.NewProviderError("", http.StatusBadGateway, "compile guardrails: "+err.Error(), err)
}
