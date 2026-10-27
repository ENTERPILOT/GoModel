package run

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/app"
)

// configCommand is the CLI word that selects the config subcommands.
const configCommand = "config"

func configUsage(productName string) string {
	return fmt.Sprintf(`Usage:
  %[1]s config export > gomodel-export.yaml

Prints the configuration a gateway started now would run with as a
config.yaml: every setting, from defaults, config.yaml, and environment
variables, and the objects saved in the dashboard. Load it as
config/config.yaml to run the same gateway from a file.

No secret is printed. A secret is written as the reference it was configured
with (${env:...}, ${file:...}, ${vault:...}), or as ${env:NAME} with a
comment naming the variable to set.

Reads the same configuration, environment, and database as the gateway, and
is safe to run while it is running. Do not redirect the output onto the
config.yaml it reads: the shell empties that file before the command starts.
`, productName)
}

// parseConfigArgs parses `config <action> ...`. It reports false with a nil
// error when help was printed.
func parseConfigArgs(productName string, args []string, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 {
		fmt.Fprint(stderr, configUsage(productName))
		return false, errors.New("config: missing action (export)")
	}
	switch args[0] {
	case "export":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, configUsage(productName))
		return false, nil
	default:
		fmt.Fprint(stderr, configUsage(productName))
		return false, fmt.Errorf("config: unknown action %q (export)", args[0])
	}

	flags := flag.NewFlagSet(productName+" config export", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, configUsage(productName)) }
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, nil
		}
		return false, err
	}
	if flags.NArg() > 0 {
		return false, fmt.Errorf("config export: unexpected arguments: %v", flags.Args())
	}
	return true, nil
}

// runConfigExport loads the configuration the way a gateway start does,
// including the distribution's SetupConfig hook (which may register secret
// schemes and a key wrapper), then prints the export to stdout. Logs go to
// stderr, so the output can be redirected to a file as is.
func runConfigExport(ctx context.Context, opts Options) error {
	// By now the shell has emptied a config file redirected onto: exporting
	// it would print defaults in place of the configuration it held.
	if f, ok := opts.Stdout.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode().IsRegular() && config.IsConfigFile(info) {
			return errors.New("config export: the output file is the config file it reads, which the shell has already emptied; restore it and write the export to another file")
		}
	}
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
	// As at gateway start: after the hook, so schemes it registered resolve.
	if err := result.ResolveSecrets(ctx); err != nil {
		return fmt.Errorf("failed to resolve secret references: %w", err)
	}
	data, err := app.ExportConfig(ctx, app.ConfigExportOptions{
		Config:     result,
		Factory:    defaultProviderFactory(result.Config),
		Extensions: opts.Extensions,
	})
	if err != nil {
		return fmt.Errorf("config export: %w", err)
	}
	_, err = opts.Stdout.Write(data)
	return err
}
