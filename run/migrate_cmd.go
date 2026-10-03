package run

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/enterpilot/gomodel/internal/litellmmigrate"
)

// migrateCommand is the CLI word that selects the migration subcommands.
const migrateCommand = "migrate"

// Files written by `migrate litellm --out`.
const (
	migratedConfigFile = "config.yaml"
	migratedEnvFile    = ".env"
	migrationReport    = "MIGRATION_REPORT.md"
)

func migrateUsage(productName string) string {
	return fmt.Sprintf(`Usage:
  %[1]s migrate litellm [--out DIR] [--force] <litellm-config.yaml>

Converts a LiteLLM proxy config.yaml into a GoModel config.yaml.

Without --out it is a dry run: it prints the migration report and the
generated config, and writes nothing. With --out it writes config.yaml,
.env (only when the LiteLLM config held inline secrets), and
MIGRATION_REPORT.md into DIR.

API keys, teams, users, budgets, and spend live in the LiteLLM database and
are not part of config.yaml; this command does not migrate them.
`, productName)
}

type migrateOptions struct {
	Source string
	Out    string
	Force  bool
}

// runMigrateCommand dispatches `gomodel migrate <source> ...`. Usage errors
// are returned as usageError so ExitCode maps them to 2.
func runMigrateCommand(productName string, args []string, stdout, stderr io.Writer) error {
	usage := func(err error) error {
		fmt.Fprint(stderr, migrateUsage(productName))
		fmt.Fprintln(stderr, err)
		return &usageError{err: err}
	}
	if len(args) == 0 {
		return usage(errors.New("migrate: missing source (litellm)"))
	}
	switch args[0] {
	case "litellm":
		opts, err := parseMigrateArgs(productName, args[1:], stderr)
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			fmt.Fprintln(stderr, err)
			return &usageError{err: err}
		}
		if err := migrateLiteLLM(opts, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return err
		}
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, migrateUsage(productName))
		return nil
	}
	return usage(fmt.Errorf("migrate: unknown source %q (litellm)", args[0]))
}

// parseMigrateArgs accepts flags before or after the config file argument.
func parseMigrateArgs(productName string, args []string, stderr io.Writer) (migrateOptions, error) {
	var opts migrateOptions
	flags := flag.NewFlagSet(productName+" migrate litellm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, migrateUsage(productName)) }
	flags.StringVar(&opts.Out, "out", "", "Directory to write config.yaml, .env, and MIGRATION_REPORT.md into (default: dry run)")
	flags.BoolVar(&opts.Force, "force", false, "Overwrite files that already exist in --out")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() == 0 {
		return opts, errors.New("migrate litellm: missing LiteLLM config file argument")
	}
	opts.Source = flags.Arg(0)
	if err := flags.Parse(flags.Args()[1:]); err != nil {
		return opts, err
	}
	if flags.NArg() > 0 {
		return opts, fmt.Errorf("migrate litellm: unexpected arguments: %v", flags.Args())
	}
	return opts, nil
}

type outputFile struct {
	name string
	data []byte
	mode fs.FileMode
}

func migrateLiteLLM(opts migrateOptions, stdout io.Writer) error {
	result, err := litellmmigrate.ConvertFile(opts.Source)
	if err != nil {
		return fmt.Errorf("migrate litellm: %w", err)
	}
	if opts.Out == "" {
		fmt.Fprint(stdout, result.Report.Markdown())
		fmt.Fprintf(stdout, "\n---\n\n%s\n", result.Config)
		if result.Env != nil {
			fmt.Fprintf(stdout, "# .env (values hidden): %v\n", result.Report.WrittenEnv)
		}
		fmt.Fprintln(stdout, "Dry run: nothing was written. Run again with --out DIR to write the files.")
		return nil
	}

	files := []outputFile{
		{migratedConfigFile, result.Config, 0o644},
		{migrationReport, []byte(result.Report.Markdown()), 0o644},
	}
	if result.Env != nil {
		files = append(files, outputFile{migratedEnvFile, result.Env, 0o600})
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return fmt.Errorf("migrate litellm: %w", err)
	}
	if !opts.Force {
		for _, f := range files {
			path := filepath.Join(opts.Out, f.name)
			// Lstat, so a dangling symlink counts as an existing file.
			if _, err := os.Lstat(path); err == nil {
				return fmt.Errorf("migrate litellm: %s already exists; pass --force to overwrite", path)
			}
		}
	}
	for _, f := range files {
		path := filepath.Join(opts.Out, f.name)
		if err := replaceFile(path, f.data, f.mode); err != nil {
			return fmt.Errorf("migrate litellm: %w", err)
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
	}
	report := result.Report
	fmt.Fprintf(stdout, "%d providers, %d virtual models, %d warnings, %d settings not migrated; see %s\n",
		len(report.Providers), len(report.VirtualModels),
		report.Count(litellmmigrate.SeverityWarning), report.Count(litellmmigrate.SeveritySkipped),
		filepath.Join(opts.Out, migrationReport))
	return nil
}

// replaceFile writes data to a private temporary file next to path, sets its
// mode, and renames it over path. Secrets are never readable under an
// overwritten file's looser mode, and a symlink at path is replaced rather
// than followed.
func replaceFile(path string, data []byte, mode fs.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
