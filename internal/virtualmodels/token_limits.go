package virtualmodels

import "github.com/enterpilot/gomodel/internal/core"

// tokenLimits returns the context window and max output tokens a redirect is
// listed with; zero means unknown. A limit configured on the redirect wins.
// Otherwise it is the smallest value reported by any declared target,
// descending enabled chained virtual models (which apply their own configured
// limits first): any target may serve a request, so a client sizing prompts
// to the smallest one never overflows the one it gets. Declared targets count
// whether or not they are available right now, so the listed limits do not
// flap with provider health. Targets reporting no value are ignored. Chains
// are acyclic by construction (see validateChains), so this terminates.
func (s *snapshot) tokenLimits(entry *redirectEntry, catalog Catalog) (contextWindow, maxOutput int) {
	configuredContext, configuredOutput := entry.vm.ContextWindow, entry.vm.MaxOutputTokens
	if configuredContext == nil || configuredOutput == nil {
		for _, target := range entry.targets {
			targetContext, targetOutput := s.targetTokenLimits(entry, target, catalog)
			contextWindow = minKnownLimit(contextWindow, targetContext)
			maxOutput = minKnownLimit(maxOutput, targetOutput)
		}
	}
	if configuredContext != nil {
		contextWindow = *configuredContext
	}
	if configuredOutput != nil {
		maxOutput = *configuredOutput
	}
	return contextWindow, maxOutput
}

// targetTokenLimits returns the token limits behind one target of owner: the
// catalog metadata of a concrete model, or the limits of the enabled redirect
// it chains into. A disabled chained redirect never serves, so it reports none.
func (s *snapshot) targetTokenLimits(owner *redirectEntry, target resolvedTarget, catalog Catalog) (contextWindow, maxOutput int) {
	if inner, ok := s.chained(owner.vm.Source, target); ok {
		if !inner.vm.Enabled {
			return 0, 0
		}
		return s.tokenLimits(inner, catalog)
	}
	model, ok := catalog.LookupModel(target.qualified)
	if !ok || model == nil || model.Metadata == nil {
		return 0, 0
	}
	return positiveLimit(model.Metadata.ContextWindow), positiveLimit(model.Metadata.MaxOutputTokens)
}

// applyTokenLimits sets a listed redirect's token limits on its metadata,
// copying the metadata first because it is shared with the catalog entry.
func applyTokenLimits(model *core.Model, contextWindow, maxOutput int) {
	if contextWindow <= 0 && maxOutput <= 0 {
		return
	}
	metadata := model.Metadata.Clone()
	if metadata == nil {
		metadata = &core.ModelMetadata{}
	}
	if contextWindow > 0 {
		metadata.ContextWindow = &contextWindow
	}
	if maxOutput > 0 {
		metadata.MaxOutputTokens = &maxOutput
	}
	model.Metadata = metadata
}

// minKnownLimit returns the smaller of two limits, treating zero as unknown.
func minKnownLimit(current, candidate int) int {
	if candidate <= 0 {
		return current
	}
	if current <= 0 {
		return candidate
	}
	return min(current, candidate)
}

func positiveLimit(value *int) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}
