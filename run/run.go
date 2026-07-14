// Package run exposes the complete GoModel gateway lifecycle as an
// importable entry point. External modules build custom gateway binaries by
// registering extensions (see the ext package) and calling Run:
//
//	func main() {
//		ext.RegisterRewriter(myRewriter{})
//		err := run.Run(context.Background(), run.Options{ProductName: "my-gateway"})
//		if code := run.ExitCode(err); code != 0 {
//			os.Exit(code)
//		}
//	}
package run

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/app"
	"github.com/enterpilot/gomodel/internal/version"
)

var shutdownTimeout = 30 * time.Second

// Options configures a gateway run. The zero value runs the standard gomodel
// gateway on os.Args.
type Options struct {
	// ProductName names the binary in CLI usage output, the startup log line,
	// and --version output. Default: "gomodel".
	ProductName string
	// Extensions is the extension registry snapshotted at server
	// construction. Default: ext.Default.
	Extensions *ext.Registry
	// Args are the CLI arguments (without the program name). Default: os.Args[1:].
	Args []string
	// Stdout and Stderr default to os.Stdout and os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
	// ConfigureSwaggerDocs receives the configured server base path so the
	// caller's generated swagger docs package can be aligned with it. The
	// gomodel binary passes its build-tagged implementation. Default: no-op.
	ConfigureSwaggerDocs func(basePath string)
	// Setup, when set, runs once the process is committed to starting the
	// gateway — after CLI parsing, --version/--health/--ready
	// short-circuits, dotenv loading, and logging configuration, but before
	// config loading. Register extensions here so operator tooling modes
	// stay silent. A returned error aborts startup.
	Setup func(ctx context.Context) error
}

func (o Options) withDefaults() Options {
	if o.ProductName == "" {
		o.ProductName = "gomodel"
	}
	if o.Extensions == nil {
		o.Extensions = ext.Default
	}
	if o.Args == nil {
		o.Args = os.Args[1:]
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.ConfigureSwaggerDocs == nil {
		o.ConfigureSwaggerDocs = func(string) {}
	}
	return o
}

// usageError marks CLI usage errors so ExitCode can map them to exit code 2.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// ExitCode maps a Run error to a process exit code: nil is 0, CLI usage
// errors are 2, everything else is 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

// Run executes the full gateway lifecycle: CLI parsing, --version and
// --health/--ready probe modes, dotenv loading, logging setup, config
// loading, provider registration, application construction (including
// registered extensions), signal handling, and start with graceful shutdown.
// Cancelling ctx triggers the same graceful shutdown as SIGINT/SIGTERM.
func Run(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()

	cliOpts, err := parseCLI(opts.ProductName, opts.Args, opts.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &usageError{err: err}
	}

	if cliOpts.Version {
		fmt.Fprintln(opts.Stdout, versionLine(opts.ProductName))
		return nil
	}

	_ = godotenv.Load()

	if cliOpts.Health {
		if err := runHealthProbe(cliOpts.HealthTimeout); err != nil {
			fmt.Fprintf(opts.Stderr, "health check failed: %v\n", err)
			return err
		}
		return nil
	}

	if cliOpts.Ready {
		if err := runReadyProbe(cliOpts.ReadyTimeout); err != nil {
			fmt.Fprintf(opts.Stderr, "readiness check failed: %v\n", err)
			return err
		}
		return nil
	}

	if err := configureLogging(opts.Stderr); err != nil {
		fmt.Fprintf(opts.Stderr, "failed to configure logging: %v\n", err)
		return err
	}

	slog.Info("starting "+opts.ProductName,
		"version", version.Version,
		"commit", version.Commit,
		"build_date", version.Date,
	)

	if opts.Setup != nil {
		if err := opts.Setup(ctx); err != nil {
			slog.Error("setup failed", "error", err)
			return err
		}
	}

	result, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return err
	}
	opts.ConfigureSwaggerDocs(result.Config.Server.BasePath)

	application, err := app.New(ctx, app.Config{
		AppConfig:  result,
		Factory:    defaultProviderFactory(result.Config),
		Extensions: opts.Extensions,
	})
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		return err
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-signalCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := shutdownApplication(application, shutdownCtx); err != nil {
			slog.Error("application shutdown error", "error", err)
		}
	}()

	addr := ":" + result.Config.Server.Port
	if err := startApplication(application, addr); err != nil {
		slog.Error("application failed", "error", err)
		return err
	}
	return nil
}

func versionLine(productName string) string {
	return fmt.Sprintf("%s %s (commit: %s, built: %s, %s)",
		productName, version.Version, version.Commit, version.Date, runtime.Version())
}

type lifecycleApp interface {
	Start(ctx context.Context, addr string) error
	Shutdown(ctx context.Context) error
}

func shutdownApplication(application lifecycleApp, ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		done <- application.Shutdown(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startApplication calls lifecycleApp.Start and, if Start fails, attempts a
// graceful shutdown via shutdownApplication using shutdownTimeout before
// returning the original start error or a combined start/shutdown error.
func startApplication(application lifecycleApp, addr string) error {
	if err := application.Start(context.Background(), addr); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if shutdownErr := shutdownApplication(application, shutdownCtx); shutdownErr != nil {
			return fmt.Errorf("server failed to start: %w", errors.Join(err, fmt.Errorf("shutdown after start failure: %w", shutdownErr)))
		}
		return err
	}
	return nil
}
