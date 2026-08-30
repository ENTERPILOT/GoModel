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
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/app"
	"github.com/enterpilot/gomodel/internal/version"
)

var shutdownTimeout = 30 * time.Second

// Distribution names for Options.AppName. They decide which release manifest
// the daily update check reads and what the X-GoModel-App header carries.
const (
	// AppCore is the open-source gateway; its checks read core.txt.
	AppCore = version.AppCore
	// AppPro is GoModel Pro; its checks read pro.txt.
	AppPro = version.AppPro
)

// Options configures a gateway run. The zero value runs the standard gomodel
// gateway on os.Args.
type Options struct {
	// ProductName names the binary in CLI usage output, the startup log line,
	// --version output, and the default OpenTelemetry service.name. Default:
	// "gomodel".
	ProductName string
	// AppName names the distribution in the X-GoModel-App header and decides
	// which release manifest the update check reads ("core.txt" or
	// "pro.txt"). Custom distributions set AppPro or their own name.
	//
	// Empty leaves version.App as the build stamped it, so a distribution can
	// choose either mechanism: this field, or -ldflags on version.App the way
	// the Pro image already stamps version.Version. Setting it here wins.
	// Default: version.AppCore ("GoModel").
	AppName string
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
	// SetupConfig runs once after the initial configuration has been loaded and
	// before the application is constructed. It lets custom distributions
	// decode their opaque extensions: configuration and register corresponding
	// extensions. Configuration registered here is startup-only; reloads reuse
	// the resulting extension instances.
	SetupConfig func(ctx context.Context, result *config.LoadResult) error
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
	if _, ok := errors.AsType[*usageError](err); ok {
		return 2
	}
	return 1
}

// Run executes the full gateway lifecycle: CLI parsing, --version,
// --health/--ready probe and --reload signalling modes, dotenv loading,
// logging setup, config loading, provider registration, application
// construction (including registered extensions), signal handling, and start
// with graceful shutdown.
//
// Cancelling ctx triggers the same graceful shutdown as SIGINT/SIGTERM. A
// reload signal (SIGHUP, what `gomodel --reload` sends) instead re-reads the
// environment file and the configuration and replaces the running application
// with one built from them, without giving up the listening socket.
func Run(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()
	// Set before anything reads it: --version output, the startup log line,
	// and the update check all report the distribution name. Assigned only
	// when supplied, so an unset field leaves a -ldflags stamp intact rather
	// than silently resetting it to the open-core name.
	if opts.AppName != "" {
		version.App = opts.AppName
	}

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

	env := newDotenv()
	env.apply() // startup has nothing to roll back to

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

	if cliOpts.Reload {
		if err := sendReloadSignal(opts.Stdout); err != nil {
			fmt.Fprintf(opts.Stderr, "reload failed: %v\n", err)
			return err
		}
		return nil
	}

	demoMode, err := demoModeFromEnv()
	if err != nil {
		fmt.Fprintln(opts.Stderr, err)
		return err
	}

	if err := configureLogging(opts.Stderr); err != nil {
		fmt.Fprintf(opts.Stderr, "failed to configure logging: %v\n", err)
		return err
	}
	if demoMode {
		demoCtx, stopDemoWarnings := context.WithCancel(ctx)
		defer stopDemoWarnings()
		startDemoModeWarnings(demoCtx)
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

	setupConfigDone := false

	// build produces one generation of the gateway from the configuration as it
	// stands right now. It is called again for every reload, which is what lets
	// a reload re-read every configuration value rather than a hand-picked
	// subset — everything except what is fixed for the life of the process (see
	// warnAboutStartupOnlySettings).
	build := func() (*app.App, *config.Config, error) {
		result, err := config.Load()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load config: %w", err)
		}
		if !setupConfigDone && opts.SetupConfig != nil {
			if err := opts.SetupConfig(ctx, result); err != nil {
				return nil, nil, fmt.Errorf("failed to set up configured extensions: %w", err)
			}
			setupConfigDone = true
		}
		opts.ConfigureSwaggerDocs(result.Config.Server.BasePath)

		application, err := app.New(ctx, app.Config{
			AppConfig:   result,
			Factory:     defaultProviderFactory(result.Config),
			Extensions:  opts.Extensions,
			DemoMode:    demoMode,
			ProductName: opts.ProductName,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize application: %w", err)
		}
		return application, result.Config, nil
	}

	application, appCfg, err := build()
	if err != nil {
		slog.Error("startup failed", "error", err)
		return err
	}

	socket, err := listenOn(":" + appCfg.Server.Port)
	if err != nil {
		slog.Error("failed to bind the server address", "error", err)
		_ = shutdownApplicationWithTimeout(application)
		return err
	}
	defer func() { _ = socket.Close() }()

	// Claim the reload signal before the pid file exists. The pid file is what
	// tells an operator or a process manager that this instance can be signalled,
	// and until Notify runs, SIGHUP still carries its default disposition: it
	// would kill the gateway instead of reloading it.
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, reloadSignal)
	defer signal.Stop(reload)

	removePIDFile, err := writePIDFile(appCfg.Server.PIDFile)
	if err != nil {
		slog.Warn("could not write the pid file; --reload will not find this instance", "error", err)
	}
	defer removePIDFile()

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// rebuild prepares the next generation. Reading the environment file and
	// installing the new logging configuration have to happen first — config.Load
	// reads the process environment, and the new log level applies to the build
	// itself — but both are process-wide, so a build that fails would otherwise
	// leave the generation that keeps serving running under a configuration the
	// operator was told had been rejected. Everything is put back instead.
	rebuild := func() (lifecycleApp, error) {
		// Variables exported into the process keep winning over the file.
		undoEnv := env.apply()
		previousLogger := slog.Default()
		rollback := func() {
			slog.SetDefault(previousLogger)
			undoEnv()
		}

		if err := configureLogging(opts.Stderr); err != nil {
			rollback()
			return nil, err
		}
		next, nextCfg, err := build()
		if err != nil {
			rollback()
			return nil, err
		}
		warnAboutStartupOnlySettings(appCfg.Server, nextCfg.Server)
		appCfg = nextCfg
		return next, nil
	}

	if err := serveUntilShutdown(signalCtx, reload, socket, application, rebuild); err != nil {
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
	StartWithListener(ctx context.Context, listener net.Listener) error
	Shutdown(ctx context.Context) error
}

// serveUntilShutdown serves the gateway until it is asked to stop, replacing
// the running application with a freshly configured one every time a reload
// signal arrives.
//
// A reload builds its replacement before stopping the generation it replaces,
// so a configuration that fails to load or to initialize leaves the gateway
// serving on the one that already works — nginx's rule, and the reason the
// operator can reload without holding their breath.
func serveUntilShutdown(ctx context.Context, reload <-chan os.Signal, socket *boundSocket, application lifecycleApp, rebuild func() (lifecycleApp, error)) error {
	for {
		listener, err := socket.next()
		if err != nil {
			_ = shutdownApplicationWithTimeout(application)
			return err
		}

		generationCtx, endGeneration := context.WithCancel(ctx)
		replacement := watchForReload(generationCtx, endGeneration, reload, rebuild)

		startErr := serveGeneration(generationCtx, application, listener)
		endGeneration()

		next := <-replacement
		switch {
		case next == nil:
			return startErr
		case startErr != nil || ctx.Err() != nil:
			// The gateway is on its way out anyway, so the replacement built
			// alongside the shutdown never gets to serve.
			_ = shutdownApplicationWithTimeout(next)
			return startErr
		}
		application = next
		slog.Info("configuration reloaded")
	}
}

// watchForReload turns reload signals into the next application generation. It
// builds the replacement first and ends the running generation only once that
// succeeded, so a failed build costs a log line rather than an outage. The
// returned channel yields the replacement, or nil when the generation ended for
// any other reason.
func watchForReload(ctx context.Context, endGeneration context.CancelFunc, reload <-chan os.Signal, rebuild func() (lifecycleApp, error)) <-chan lifecycleApp {
	replacement := make(chan lifecycleApp, 1)
	go func() {
		defer close(replacement)
		for {
			select {
			case <-ctx.Done():
				return
			case <-reload:
				slog.Info("reloading configuration")
				next, err := rebuild()
				if err != nil {
					slog.Error("reload failed; keeping the running configuration", "error", err)
					continue
				}
				replacement <- next
				endGeneration()
				return
			}
		}
	}()
	return replacement
}

// serveGeneration starts one application generation and returns only once its
// server has stopped *and* the teardown that stopped it has finished.
//
// The teardown has to run on its own goroutine because StartWithListener
// blocks until the server stops and Shutdown is what stops it. Waiting for that
// goroutine here is the load-bearing part: the process exits the moment Run
// returns, so anything Shutdown had not reached yet — the buffered usage and
// audit records, the database handle — would be dropped on every Ctrl+C.
//
// Every generation that serves is torn down here, exactly once and on a single
// shutdownTimeout budget, whichever way the server ended: a signal, a reload, a
// stop of its own accord, or a start that never got off the ground all converge
// here. Routing the failed-start path through the same place is what removes
// the second teardown that used to run alongside it, and with it any reliance
// on Shutdown being idempotent. An application built but never served — one
// that could not be given a listener, or a replacement overtaken by shutdown —
// is torn down by its owner in serveUntilShutdown, on the same budget.
func serveGeneration(ctx context.Context, application lifecycleApp, listener net.Listener) error {
	serverReturned := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
		case <-serverReturned:
		}
		shutdownDone <- shutdownApplicationWithTimeout(application)
	}()

	startErr := application.StartWithListener(context.Background(), listener)
	close(serverReturned)

	if err := <-shutdownDone; err != nil {
		slog.Error("application shutdown error", "error", err)
	}
	return startErr
}

// shutdownApplicationWithTimeout tears an application down on the one budget
// every teardown gets, whether or not it ever served.
func shutdownApplicationWithTimeout(application lifecycleApp) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return shutdownApplication(application, shutdownCtx)
}

// warnAboutStartupOnlySettings reports the settings a reload cannot apply: the
// listening socket is kept bound across generations precisely so no connection
// is refused, and the pid file names the process that is already running.
func warnAboutStartupOnlySettings(current, next config.ServerConfig) {
	if next.Port != current.Port {
		slog.Warn("server port change needs a restart; keeping the bound address",
			"bound_port", current.Port, "configured_port", next.Port)
	}
	if next.PIDFile != current.PIDFile {
		slog.Warn("pid file change needs a restart; keeping the current pid file",
			"current_pid_file", current.PIDFile, "configured_pid_file", next.PIDFile)
	}
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
