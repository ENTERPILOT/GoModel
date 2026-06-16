package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

const defaultHealthTimeout = 2 * time.Second

type cliOptions struct {
	Version       bool
	Health        bool
	HealthTimeout time.Duration
}

func parseCLI(args []string, output io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("gomodel", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&opts.Version, "version", false, "Print version information")
	flags.BoolVar(&opts.Health, "health", false, "Check the local GoModel health endpoint and exit")
	flags.DurationVar(&opts.HealthTimeout, "health-timeout", defaultHealthTimeout, "Timeout for --health")
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
