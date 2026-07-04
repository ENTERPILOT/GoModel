package guardrails

import "testing"

func TestComputeGuardrailsHash_Stable(t *testing.T) {
	rules := []RuleDescriptor{
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
	}
	h1 := ComputeGuardrailsHash(rules)
	h2 := ComputeGuardrailsHash(rules)
	if h1 != h2 {
		t.Fatal("hash should be stable across calls")
	}
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
	if ComputeGuardrailsHash(rules1) != ComputeGuardrailsHash(rules2) {
		t.Fatal("hash should be order-independent (rules are sorted)")
	}
}

func TestComputeGuardrailsHash_ChangesOnContentChange(t *testing.T) {
	v1 := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."}}
	v2 := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be very safe."}}
	if ComputeGuardrailsHash(v1) == ComputeGuardrailsHash(v2) {
		t.Fatal("hash should change when rule content changes")
	}
}

func TestComputeGuardrailsHash_ChangesOnRuleOrderOrMode(t *testing.T) {
	base := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "inject", Content: "Be safe."}}
	reordered := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 1, Mode: "inject", Content: "Be safe."}}
	mode := []RuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "override", Content: "Be safe."}}
	if ComputeGuardrailsHash(base) == ComputeGuardrailsHash(reordered) {
		t.Fatal("hash should change when guardrail execution order changes")
	}
	if ComputeGuardrailsHash(base) == ComputeGuardrailsHash(mode) {
		t.Fatal("hash should change when system_prompt mode changes")
	}
}
