package virtualmodels

import "github.com/enterpilot/gomodel/internal/core"

// listedLimits are the token limits a redirect is listed with; zero means
// unknown.
type listedLimits struct {
	contextWindow int
	maxOutput     int
	// maxModelLen is the context limit vLLM discovery advertises
	// (models.vllm_discovery_compat): the one the runtime reported, else the
	// context window.
	maxModelLen int
}

// smallest returns, per limit, the smaller known value of the two.
func (l listedLimits) smallest(other listedLimits) listedLimits {
	return listedLimits{
		contextWindow: minKnownLimit(l.contextWindow, other.contextWindow),
		maxOutput:     minKnownLimit(l.maxOutput, other.maxOutput),
		maxModelLen:   minKnownLimit(l.maxModelLen, other.maxModelLen),
	}
}

// tokenLimits returns the limits a redirect is listed with. A limit configured
// on the redirect wins. Otherwise it is the smallest value reported by any
// declared target, descending enabled chained virtual models (which apply
// their own configured limits first): any target may serve a request, so a
// client sizing prompts to the smallest one never overflows the one it gets.
// Declared targets count whether or not they are available right now, so the
// listed limits do not flap with provider health. Targets reporting no value
// are ignored, so the listed limits do not bound them; operators configure a
// value then. Chains are acyclic by construction (see validateChains), so this
// terminates.
func (s *snapshot) tokenLimits(entry *redirectEntry, catalog Catalog) listedLimits {
	var limits listedLimits
	configuredContext, configuredOutput := entry.vm.ContextWindow, entry.vm.MaxOutputTokens
	if configuredContext == nil || configuredOutput == nil {
		for _, target := range entry.targets {
			limits = limits.smallest(s.targetTokenLimits(entry, target, catalog))
		}
	}
	if configuredContext != nil {
		limits.contextWindow = *configuredContext
		limits.maxModelLen = *configuredContext
	}
	if configuredOutput != nil {
		limits.maxOutput = *configuredOutput
	}
	return limits
}

// targetTokenLimits returns the token limits behind one target of owner: those
// the catalog holds for a concrete model, or the limits of the enabled redirect
// it chains into. A disabled chained redirect never serves, so it reports none.
func (s *snapshot) targetTokenLimits(owner *redirectEntry, target resolvedTarget, catalog Catalog) listedLimits {
	if inner, ok := s.chained(owner.vm.Source, target); ok {
		if !inner.vm.Enabled {
			return listedLimits{}
		}
		return s.tokenLimits(inner, catalog)
	}
	model, ok := catalog.LookupModel(target.qualified)
	if !ok || model == nil {
		return listedLimits{}
	}
	var limits listedLimits
	if model.Metadata != nil {
		limits.contextWindow = positiveLimit(model.Metadata.ContextWindow)
		limits.maxOutput = positiveLimit(model.Metadata.MaxOutputTokens)
	}
	limits.maxModelLen = positiveLimit(model.MaxModelLen)
	if limits.maxModelLen == 0 {
		limits.maxModelLen = limits.contextWindow
	}
	return limits
}

// applyTokenLimits sets a listed redirect's token limits on its model entry,
// copying the metadata first because it is shared with the catalog entry. An
// unknown limit is omitted rather than left as the representative target's
// value.
func applyTokenLimits(model *core.Model, limits listedLimits) {
	model.MaxModelLen = knownLimit(limits.maxModelLen)
	if model.Metadata == nil && limits.contextWindow <= 0 && limits.maxOutput <= 0 {
		return
	}
	metadata := model.Metadata.Clone()
	if metadata == nil {
		metadata = &core.ModelMetadata{}
	}
	metadata.ContextWindow = knownLimit(limits.contextWindow)
	metadata.MaxOutputTokens = knownLimit(limits.maxOutput)
	model.Metadata = metadata
}

// knownLimit returns a pointer to a positive limit, or nil when it is unknown.
func knownLimit(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
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
