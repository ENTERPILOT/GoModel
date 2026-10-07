package run

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

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
  %[1]s migrate litellm [--out DIR] [--force] [--database-url URL | --skip-database]
      [--gomodel-url URL [--allow-http]] <litellm-config.yaml>

Converts a LiteLLM proxy config.yaml into a GoModel config.yaml, and reads
virtual keys, teams, users, budgets, and rate limits from the LiteLLM
database.

Without --out nothing is written to disk: it prints the migration report and
the generated config. With --out it writes config.yaml, .env (only when the
LiteLLM config held inline secrets), and MIGRATION_REPORT.md into DIR.

The database is read when a URL is known: --database-url, else the config's
general_settings.database_url, else DATABASE_URL. Reads are read-only.
Without a URL, only the config is converted.

--gomodel-url imports what was read into a running GoModel through its
admin API, authenticating with GOMODEL_MASTER_KEY or else the LiteLLM
master key. Start GoModel with the converted config first. Re-running
updates what an earlier run imported, and deactivates keys LiteLLM has
since blocked. A --gomodel-url on another machine must use https unless
--allow-http is passed.
`, productName)
}

type migrateOptions struct {
	Source       string
	Out          string
	Force        bool
	DatabaseURL  string
	SkipDatabase bool
	GoModelURL   string
	AllowHTTP    bool
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
	flags.StringVar(&opts.DatabaseURL, "database-url", "", "LiteLLM PostgreSQL URL (default: general_settings.database_url, else DATABASE_URL)")
	flags.BoolVar(&opts.SkipDatabase, "skip-database", false, "Convert the config only, without reading the LiteLLM database")
	flags.StringVar(&opts.GoModelURL, "gomodel-url", "", "Import keys, teams, and budgets into the GoModel running at this URL")
	flags.BoolVar(&opts.AllowHTTP, "allow-http", false, "Send the admin key over plain HTTP to a --gomodel-url that is not on this machine")
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
	if opts.SkipDatabase && (opts.DatabaseURL != "" || opts.GoModelURL != "") {
		return opts, errors.New("migrate litellm: --skip-database cannot be combined with --database-url or --gomodel-url")
	}
	if opts.GoModelURL != "" {
		if err := checkGoModelURL(opts.GoModelURL, opts.AllowHTTP); err != nil {
			return opts, fmt.Errorf("migrate litellm: %w", err)
		}
	}
	return opts, nil
}

// checkGoModelURL refuses to send the admin key over plain HTTP to another
// machine unless the operator allows it, as on a private Compose network.
func checkGoModelURL(raw string, allowHTTP bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("--gomodel-url %q is not a URL such as http://localhost:8080", raw)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if allowHTTP || host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("--gomodel-url %s would send the admin key unencrypted; use https, or pass --allow-http on a trusted network", raw)
	}
	return fmt.Errorf("--gomodel-url %q must use http or https", raw)
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
	plan, err := planDatabaseImport(opts, result)
	if err != nil {
		return fmt.Errorf("migrate litellm: %w", err)
	}
	if opts.Out == "" {
		fmt.Fprint(stdout, result.Report.Markdown())
		fmt.Fprintf(stdout, "\n---\n\n%s\n", result.Config)
		if result.Env != nil {
			fmt.Fprintf(stdout, "# .env (values hidden): %v\n", result.Report.WrittenEnv)
		}
		fmt.Fprintln(stdout, "No files were written. Run again with --out DIR to write them.")
	} else if err := writeMigratedFiles(opts, result, stdout); err != nil {
		return err
	}
	if opts.GoModelURL == "" {
		if plan != nil {
			fmt.Fprintln(stdout, "Nothing was imported into GoModel. Start it with the converted config, then run again with --gomodel-url URL.")
		}
		return nil
	}
	return applyDatabaseImport(opts, result, plan, stdout)
}

// planDatabaseImport reads the LiteLLM database when a URL is known and
// plans the import. It returns nil when the database is not read.
func planDatabaseImport(opts migrateOptions, result *litellmmigrate.Result) (*litellmmigrate.ImportPlan, error) {
	url := cmp.Or(opts.DatabaseURL, result.DatabaseURL, os.Getenv("DATABASE_URL"))
	if opts.SkipDatabase || url == "" {
		if opts.GoModelURL != "" {
			return nil, errors.New("--gomodel-url imports from the LiteLLM database; pass --database-url")
		}
		result.Report.Findings = append(result.Report.Findings, litellmmigrate.Finding{
			Severity: litellmmigrate.SeveritySkipped,
			Subject:  "database",
			Message:  "not read: keys, teams, users, and budgets were not migrated. Pass --database-url to import them",
		})
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := litellmmigrate.ReadDatabase(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("%w (pass --skip-database to convert the config only)", err)
	}
	return litellmmigrate.PlanImport(db, result, time.Now()), nil
}

func applyDatabaseImport(opts migrateOptions, result *litellmmigrate.Result, plan *litellmmigrate.ImportPlan, stdout io.Writer) error {
	adminKey := cmp.Or(os.Getenv("GOMODEL_MASTER_KEY"), result.MasterKey)
	applied, err := litellmmigrate.Apply(context.Background(), adminClient(), opts.GoModelURL, adminKey, plan)
	if err != nil {
		return fmt.Errorf("migrate litellm: %w", err)
	}
	fmt.Fprintf(stdout, "imported into %s: %d new keys, %d keys updated, %d model policies, %d budgets, %d rate limits\n",
		opts.GoModelURL, applied.Keys, applied.KeysUpdated, applied.Policies, applied.Budgets, applied.RateLimits)
	for _, failure := range applied.Failures {
		fmt.Fprintf(stdout, "failed: %s\n", failure)
	}
	if len(applied.Failures) > 0 {
		return fmt.Errorf("migrate litellm: %d imports failed; fix the cause and run again", len(applied.Failures))
	}
	return nil
}

// adminClient is the HTTP client that carries the admin key. It never
// follows a redirect from https to http: Go keeps the Authorization header
// on a same-host redirect, so that would send the key unencrypted.
func adminClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing a redirect to %s: it would send the admin key unencrypted", req.URL.Redacted())
			}
			return nil
		},
	}
}

func writeMigratedFiles(opts migrateOptions, result *litellmmigrate.Result, stdout io.Writer) error {
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
