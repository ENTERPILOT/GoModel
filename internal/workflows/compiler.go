package workflows

import (
	"errors"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
)

type compiler struct {
	registry    guardrails.Catalog
	featureCaps core.WorkflowFeatures
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
	if c == nil {
		return nil, core.NewProviderError("", http.StatusBadGateway, "workflow compiler is not configured", nil)
	}
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

	legacyHeaderPolicies := make(map[string]struct{})
	if c.registry != nil {
		for _, name := range c.registry.HeaderPolicyNames() {
			legacyHeaderPolicies[name] = struct{}{}
		}
	}

	var pipeline *guardrails.Pipeline
	if policy.Features.Guardrails {
		steps := make([]guardrails.StepReference, 0, len(version.Payload.Guardrails))
		for _, step := range version.Payload.Guardrails {
			if _, legacy := legacyHeaderPolicies[step.Ref]; legacy {
				continue
			}
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

	headerSteps := make([]guardrails.StepReference, 0, len(version.Payload.HeaderPolicies)+len(version.Payload.Guardrails))
	seenHeaderPolicies := make(map[string]struct{})
	for _, step := range version.Payload.HeaderPolicies {
		headerSteps = append(headerSteps, guardrails.StepReference{Ref: step.Ref, Step: step.Step})
		seenHeaderPolicies[step.Ref] = struct{}{}
	}
	// Compatibility: workflow versions authored by the preview stored header
	// policies in guardrails. Compile them into the egress stage, but only when
	// that legacy workflow enabled guardrails (matching its former behavior).
	if policy.Features.Guardrails {
		for _, step := range version.Payload.Guardrails {
			if _, legacy := legacyHeaderPolicies[step.Ref]; !legacy {
				continue
			}
			if _, duplicate := seenHeaderPolicies[step.Ref]; duplicate {
				continue
			}
			headerSteps = append(headerSteps, guardrails.StepReference{Ref: step.Ref, Step: step.Step})
		}
	}
	headerPolicies, _, err := c.compileHeaderPolicies(headerSteps)
	if err != nil {
		return nil, err
	}

	return &CompiledWorkflow{
		Version:        version,
		Policy:         policy,
		Pipeline:       pipeline,
		HeaderPolicies: headerPolicies,
	}, nil
}

func (c *compiler) compileHeaderPolicies(steps []guardrails.StepReference) ([]core.HeaderPolicy, string, error) {
	if len(steps) == 0 {
		return nil, "", nil
	}
	if c == nil || c.registry == nil {
		return nil, "", core.NewProviderError("", http.StatusBadGateway, "header policies are configured but no policy catalog is available", nil)
	}
	policies, hash, err := c.registry.BuildHeaderPolicies(steps)
	if err == nil {
		return policies, hash, nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return nil, "", gatewayErr
	}
	return nil, "", core.NewProviderError("", http.StatusBadGateway, "compile header policies: "+err.Error(), err)
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
