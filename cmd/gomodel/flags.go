package main

import (
	"errors"
	"flag"
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
	return opts, flags.Parse(args)
}

func cliParseExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
