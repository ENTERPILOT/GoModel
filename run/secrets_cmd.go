package run

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/app"
)

// secretsCommand is the CLI word that selects the secrets subcommands.
const secretsCommand = "secrets"

func secretsUsage(productName string) string {
	return fmt.Sprintf(`Usage:
  %[1]s secrets reencrypt [--rotate-data-key]

Encrypts every dashboard-managed secret (provider credentials, MCP server
headers, guardrail secrets) that is still stored in plaintext, and re-encrypts
values sealed with an older data key under the active one. Safe to run more
than once, and while the gateway is running.

Reads the same configuration and environment as the gateway, and needs
GOMODEL_ENCRYPTION_KEY (or a key wrapper registered by the distribution).

  --rotate-data-key   create a new data key and move every secret to it
`, productName)
}

type secretsOptions struct {
	RotateDataKey bool
}

// parseSecretsArgs parses `secrets <action> ...`. A nil result with a nil
// error means help was printed.
func parseSecretsArgs(productName string, args []string, stdout, stderr io.Writer) (*secretsOptions, error) {
	if len(args) == 0 {
		fmt.Fprint(stderr, secretsUsage(productName))
		return nil, errors.New("secrets: missing action (reencrypt)")
	}
	switch args[0] {
	case "reencrypt":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, secretsUsage(productName))
		return nil, nil
	default:
		fmt.Fprint(stderr, secretsUsage(productName))
		return nil, fmt.Errorf("secrets: unknown action %q (reencrypt)", args[0])
	}

	var opts secretsOptions
	flags := flag.NewFlagSet(productName+" secrets reencrypt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, secretsUsage(productName)) }
	flags.BoolVar(&opts.RotateDataKey, "rotate-data-key", false, "Create a new data key and move every secret to it")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, err
	}
	if flags.NArg() > 0 {
		return nil, fmt.Errorf("secrets reencrypt: unexpected arguments: %v", flags.Args())
	}
	return &opts, nil
}

// runSecretsReencrypt loads the configuration the way a gateway start does,
// including the distribution's SetupConfig hook (which may register a key
// wrapper), then re-encrypts. It prints counts only, never values.
func runSecretsReencrypt(ctx context.Context, opts Options, secretsOpts secretsOptions) error {
	if opts.Setup != nil {
		if err := opts.Setup(ctx); err != nil {
			return err
		}
	}
	result, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := configHooks(ctx, opts)(result); err != nil {
		return err
	}
	outcome, err := app.ReencryptSecrets(ctx, app.ReencryptOptions{
		Config:        result,
		Extensions:    opts.Extensions,
		RotateDataKey: secretsOpts.RotateDataKey,
	})
	for _, report := range outcome.Reports {
		fmt.Fprintf(opts.Stdout, "%s: %d rows, %d re-encrypted\n", report.Entity, report.Rows, report.Reencrypted)
	}
	if err != nil {
		return fmt.Errorf("secrets reencrypt: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "active data key: %s\n", outcome.ActiveKeyID)
	return nil
}
