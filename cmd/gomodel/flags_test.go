package main

import (
	"flag"
	"fmt"
	"io"
	"testing"
)

func TestParseCLI_AcceptsSingleAndDoubleDashVersion(t *testing.T) {
	for _, args := range [][]string{{"-version"}, {"--version"}} {
		opts, err := parseCLI(args, io.Discard)
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
		opts, err := parseCLI(args, io.Discard)
		if err != nil {
			t.Fatalf("parseCLI(%v) error = %v", args, err)
		}
		if !opts.Health {
			t.Fatalf("parseCLI(%v).Health = false, want true", args)
		}
	}
}

func TestParseCLI_RejectsUnknownFlags(t *testing.T) {
	if _, err := parseCLI([]string{"--helath"}, io.Discard); err == nil {
		t.Fatal("parseCLI(--helath) error = nil, want error")
	}
}

func TestParseCLI_RejectsPositionalArgs(t *testing.T) {
	if _, err := parseCLI([]string{"--health", "extra"}, io.Discard); err == nil {
		t.Fatal("parseCLI(--health extra) error = nil, want error")
	}
}

func TestCLIParseExitCode(t *testing.T) {
	if got := cliParseExitCode(flag.ErrHelp); got != 0 {
		t.Fatalf("cliParseExitCode(flag.ErrHelp) = %d, want 0", got)
	}
	if got := cliParseExitCode(fmt.Errorf("parse flags: %w", flag.ErrHelp)); got != 0 {
		t.Fatalf("cliParseExitCode(wrapped help) = %d, want 0", got)
	}
}
