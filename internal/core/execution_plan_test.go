package core

import "testing"

func TestNewExecutionPlanSelector_DropsInvalidUserPath(t *testing.T) {
	t.Parallel()

	selector := NewExecutionPlanSelector("openai", "gpt-5", "/team/../alpha")
	if selector.UserPath != "" {
		t.Fatalf("UserPath = %q, want empty", selector.UserPath)
	}
}
