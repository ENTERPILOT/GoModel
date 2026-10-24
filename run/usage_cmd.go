package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/usage"
)

// usageCommand is the CLI word that selects the usage subcommands.
const usageCommand = "usage"

func usageCmdUsage(productName string) string {
	return fmt.Sprintf(`Usage:
  %[1]s usage import <source.db>

Copies the usage history of another GoModel instance, read from its SQLite
database file, into the database this instance is configured to use (SQLite,
PostgreSQL, or MongoDB). The dashboard then shows both instances' usage
together. The source file is opened read-only. Rows already present are
skipped, so running it again is safe.

Reads the same configuration and environment as the gateway to find the
destination database.
`, productName)
}

// parseUsageArgs parses `usage import <source.db>`. An empty source with a
// nil error means help was printed.
func parseUsageArgs(productName string, args []string, stdout, stderr io.Writer) (string, error) {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageCmdUsage(productName))
		return "", errors.New("usage: missing action (import)")
	}
	switch args[0] {
	case "import":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageCmdUsage(productName))
		return "", nil
	default:
		fmt.Fprint(stderr, usageCmdUsage(productName))
		return "", fmt.Errorf("usage: unknown action %q (import)", args[0])
	}
	switch {
	case len(args) == 2 && (args[1] == "-h" || args[1] == "--help"):
		fmt.Fprint(stdout, usageCmdUsage(productName))
		return "", nil
	case len(args) < 2 || args[1] == "":
		fmt.Fprint(stderr, usageCmdUsage(productName))
		return "", errors.New("usage import: missing source database file")
	case len(args) > 2:
		return "", fmt.Errorf("usage import: unexpected arguments: %v", args[2:])
	}
	return args[1], nil
}

// runUsageImport loads the configuration the way a gateway start does, opens
// the configured storage, and copies the source database's usage rows into it.
func runUsageImport(ctx context.Context, opts Options, source string) error {
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
	if err := result.ResolveSecrets(ctx); err != nil {
		return fmt.Errorf("failed to resolve secret references: %w", err)
	}
	backend := result.Config.Storage.BackendConfig()
	if backend.Type == storage.TypeSQLite && sameFile(source, backend.SQLite.Path) {
		return fmt.Errorf("usage import: %s is this instance's own database", source)
	}
	dst, err := storage.New(ctx, backend)
	if err != nil {
		return fmt.Errorf("usage import: open storage: %w", err)
	}
	defer dst.Close()

	n, err := usage.ImportSQLiteFile(ctx, source, dst)
	if err != nil {
		return fmt.Errorf("usage import: %w (%d rows copied before the error; run again to resume)", err, n)
	}
	fmt.Fprintf(opts.Stdout, "imported %d usage rows from %s into %s storage (rows already present were skipped)\n", n, source, backend.Type)
	return nil
}

func sameFile(a, b string) bool {
	if b == "" {
		b = storage.DefaultSQLitePath()
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}
