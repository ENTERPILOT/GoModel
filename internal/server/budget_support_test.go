package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

type countingBudgetChecker struct {
	calls    int
	userPath string
}

func (c *countingBudgetChecker) Check(_ context.Context, userPath string, _ time.Time) error {
	c.calls++
	c.userPath = userPath
	return nil
}

func TestEnforceBudgetSkipsWhenWorkflowBudgetDisabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Budget: false,
			},
		},
	}))
	c := e.NewContext(req, httptest.NewRecorder())
	checker := &countingBudgetChecker{}

	if err := enforceBudget(c, checker); err != nil {
		t.Fatalf("enforceBudget returned error: %v", err)
	}
	if checker.calls != 0 {
		t.Fatalf("budget checker was called %d times, want 0", checker.calls)
	}
}

func TestEnforceBudgetDefaultsEnabledWithoutWorkflow(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	checker := &countingBudgetChecker{}

	if err := enforceBudget(c, checker); err != nil {
		t.Fatalf("enforceBudget returned error: %v", err)
	}
	if checker.calls != 1 {
		t.Fatalf("budget checker was called %d times, want 1", checker.calls)
	}
	if checker.userPath != "/" {
		t.Fatalf("budget user path = %q, want /", checker.userPath)
	}
}

func TestBatchBudgetEnforcerUsesResolvedWorkflow(t *testing.T) {
	checker := &countingBudgetChecker{}
	enforcer := batchBudgetEnforcer(checker)
	if enforcer == nil {
		t.Fatal("batchBudgetEnforcer() = nil, want function")
	}

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: false,
			},
		},
	})

	if err := enforcer(ctx); err != nil {
		t.Fatalf("batch budget enforcer returned error: %v", err)
	}
	if checker.calls != 0 {
		t.Fatalf("budget checker was called %d times, want 0", checker.calls)
	}
}
