package run

import (
	"fmt"
	"io"
	"testing"
)

func TestParseCLI_AcceptsSingleAndDoubleDashVersion(t *testing.T) {
	for _, args := range [][]string{{"-version"}, {"--version"}} {
		opts, err := parseCLI("gomodel", args, io.Discard)
		if err != nil {
			t.Fatalf("parseCLI(%v) error = %v", args, err)
		}
		if !opts.Version {
			t.Fatalf("parseCLI(%v).Version = false, want true", args)
		}
	}
}

func TestParseCLI_AcceptsSingleAndDoubleDashHealth(t *testing.T) {
	for _, args := range [][]string{{"-health"}, {"--health"}} {
		opts, err := parseCLI("gomodel", args, io.Discard)
		if err != nil {
			t.Fatalf("parseCLI(%v) error = %v", args, err)
		}
		if !opts.Health {
			t.Fatalf("parseCLI(%v).Health = false, want true", args)
		}
	}
}

func TestParseCLI_RejectsUnknownFlags(t *testing.T) {
	if _, err := parseCLI("gomodel", []string{"--helath"}, io.Discard); err == nil {
		t.Fatal("parseCLI(--helath) error = nil, want error")
	}
}

func TestParseCLI_RejectsPositionalArgs(t *testing.T) {
	if _, err := parseCLI("gomodel", []string{"--health", "extra"}, io.Discard); err == nil {
		t.Fatal("parseCLI(--health extra) error = nil, want error")
	}
}

func TestExitCode(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", got)
	}
	if got := ExitCode(&usageError{err: fmt.Errorf("unexpected arguments")}); got != 2 {
		t.Fatalf("ExitCode(usage error) = %d, want 2", got)
	}
	if got := ExitCode(fmt.Errorf("boom")); got != 1 {
		t.Fatalf("ExitCode(generic error) = %d, want 1", got)
	}
}
