package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

const (
	defaultHealthTimeout = 2 * time.Second
	// defaultReadyTimeout is larger than the server's per-probe readinessProbeTimeout
	// so a slow dependency yields a clean not_ready/degraded response instead of
	// the client cutting the connection first.
	defaultReadyTimeout = 4 * time.Second
)

type cliOptions struct {
	Version       bool
	Health        bool
	HealthTimeout time.Duration
	Ready         bool
	ReadyTimeout  time.Duration
}

func parseCLI(args []string, output io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("gomodel", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&opts.Version, "version", false, "Print version information")
	flags.BoolVar(&opts.Health, "health", false, "Check the local GoModel health (liveness) endpoint and exit")
	flags.DurationVar(&opts.HealthTimeout, "health-timeout", defaultHealthTimeout, "Timeout for --health")
	flags.BoolVar(&opts.Ready, "ready", false, "Check the local GoModel readiness endpoint and exit")
	flags.DurationVar(&opts.ReadyTimeout, "ready-timeout", defaultReadyTimeout, "Timeout for --ready")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() > 0 {
		return opts, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return opts, nil
}

func cliParseExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
