package guardrails

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeGuardrailsHash_Stable(t *testing.T) {
	rules := []RuleDescriptor{
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
	}
	h1 := ComputeGuardrailsHash(rules)
	h2 := ComputeGuardrailsHash(rules)
	require.Equal(t, h2, h1)
}

func TestComputeGuardrailsHash_OrderIndependent(t *testing.T) {
	rules1 := []RuleDescriptor{
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
	}
	rules2 := []RuleDescriptor{
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
	}
	require.Equal(t, ComputeGuardrailsHash(rules2), ComputeGuardrailsHash(rules1))
}

func TestComputeGuardrailsHash_ChangesOnContentChange(t *testing.T) {
	v1 := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."}}
	v2 := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be very safe."}}
	require.NotEqual(t, ComputeGuardrailsHash(v2), ComputeGuardrailsHash(v1))
}

func TestComputeGuardrailsHash_ChangesOnRuleOrderOrMode(t *testing.T) {
	base := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "inject", Content: "Be safe."}}
	reordered := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 1, Mode: "inject", Content: "Be safe."}}
	mode := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "override", Content: "Be safe."}}
	require.NotEqual(t, ComputeGuardrailsHash(reordered), ComputeGuardrailsHash(base))
	require.NotEqual(t, ComputeGuardrailsHash(mode), ComputeGuardrailsHash(base))
}
