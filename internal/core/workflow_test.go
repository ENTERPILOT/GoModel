package core

import "testing"

func TestNewWorkflowSelector_DropsInvalidUserPath(t *testing.T) {
	t.Parallel()

	selector := NewWorkflowSelector("openai", "gpt-5", "/team/../alpha")
	if selector.UserPath != "" {
		t.Fatalf("UserPath = %q, want empty", selector.UserPath)
	}
}
