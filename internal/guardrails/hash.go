package guardrails

import "github.com/enterpilot/gomodel/internal/plugins"

// RuleDescriptor describes a single active guardrail rule for hashing.
type RuleDescriptor = plugins.RuleDescriptor

// ComputeGuardrailsHash computes the guardrails_hash for a set of rule
// identifiers. See plugins.ComputeChainHash.
func ComputeGuardrailsHash(rules []RuleDescriptor) string {
	return plugins.ComputeChainHash(rules)
}
