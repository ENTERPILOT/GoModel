package workflows

import (
	"errors"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

type compiler struct {
	registry    guardrails.Catalog
	featureCaps core.WorkflowFeatures
}

// NewCompilerWithFeatureCaps creates the default workflow compiler with
// process-level feature caps applied at compile time.
func NewCompilerWithFeatureCaps(registry guardrails.Catalog, featureCaps core.WorkflowFeatures) Compiler {
	return &compiler{
		registry:    registry,
		featureCaps: featureCaps,
	}
}

func (c *compiler) Compile(version Version) (*CompiledWorkflow, error) {
	features := version.Payload.Features.runtimeFeatures().ApplyUpperBound(c.featureCaps)
	policy := &core.ResolvedWorkflowPolicy{
		VersionID:     version.ID,
		Version:       version.Version,
		ScopeProvider: version.Scope.Provider,
		ScopeModel:    version.Scope.Model,
		ScopeUserPath: version.Scope.UserPath,
		Name:          version.Name,
		WorkflowHash:  version.WorkflowHash,
		Features:      features,
	}

	var chains *plugins.Chains
	if policy.Features.Guardrails {
		steps := version.Payload.EffectiveSteps()
		refs := make([]guardrails.StepReference, 0, len(steps))
		for _, step := range steps {
			refs = append(refs, guardrails.StepReference{
				Ref:   step.Ref,
				Phase: pluginapi.Kind(step.Phase),
				Step:  step.Step,
			})
		}
		var err error
		chains, err = c.compileGuardrails(refs)
		if err != nil {
			return nil, err
		}
		policy.GuardrailsHash = chains.CacheHash()
		policy.ChainHashes = chains.Hashes()
	}

	return &CompiledWorkflow{
		Version: version,
		Policy:  policy,
		Chains:  chains,
	}, nil
}

func (c *compiler) compileGuardrails(steps []guardrails.StepReference) (*plugins.Chains, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	if c == nil || c.registry == nil {
		return nil, core.NewProviderError("", http.StatusBadGateway, "guardrails are enabled but no guardrail registry is configured", nil)
	}
	if c.registry.Len() == 0 {
		return nil, core.NewProviderError("", http.StatusBadGateway, "guardrails are enabled but no guardrails are loaded", nil)
	}
	chains, err := c.registry.BuildChains(steps)
	if err == nil {
		return chains, nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return nil, gatewayErr
	}
	return nil, core.NewProviderError("", http.StatusBadGateway, "compile guardrails: "+err.Error(), err)
}
