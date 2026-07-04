package run

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunVersionSkipsSetup(t *testing.T) {
	setupCalled := false
	var stdout strings.Builder

	err := Run(context.Background(), Options{
		ProductName: "gomodel-test",
		Args:        []string{"--version"},
		Stdout:      &stdout,
		Stderr:      io.Discard,
		Setup: func(context.Context) error {
			setupCalled = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run(--version) error = %v", err)
	}
	if setupCalled {
		t.Error("Setup must not run for --version")
	}
	if !strings.HasPrefix(stdout.String(), "gomodel-test ") {
		t.Errorf("version output = %q, want prefix %q", stdout.String(), "gomodel-test ")
	}
}

func TestRunUsageErrorExitCode(t *testing.T) {
	err := Run(context.Background(), Options{
		Args:   []string{"--not-a-flag"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2", got)
	}
}

func TestRunHelpIsNotAnError(t *testing.T) {
	err := Run(context.Background(), Options{
		Args:   []string{"--help"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run(--help) error = %v, want nil", err)
	}
	if got := ExitCode(err); got != 0 {
		t.Errorf("ExitCode = %d, want 0", got)
	}
}
