package core

import "testing"

func TestNewWorkflowSelector_DropsInvalidUserPath(t *testing.T) {
	t.Parallel()

	selector := NewWorkflowSelector("openai", "gpt-5", "/team/../alpha")
	if selector.UserPath != "" {
		t.Fatalf("UserPath = %q, want empty", selector.UserPath)
	}
}

func TestWorkflowFeaturesApplyUpperBound_DisablesBudgetWhenUsageDisabled(t *testing.T) {
	t.Parallel()

	features := WorkflowFeatures{
		Usage:  true,
		Budget: true,
	}.ApplyUpperBound(WorkflowFeatures{
		Usage:  false,
		Budget: true,
	})

	if features.Usage {
		t.Fatal("ApplyUpperBound().Usage = true, want false")
	}
	if features.Budget {
		t.Fatal("ApplyUpperBound().Budget = true, want false")
	}
}
