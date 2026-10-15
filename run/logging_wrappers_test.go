package run

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordLog collects what recording wrappers saw, as "tag:message".
type recordLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *recordLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *recordLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

func (l *recordLog) count(entry string) int {
	n := 0
	for _, e := range l.snapshot() {
		if e == entry {
			n++
		}
	}
	return n
}

// recordingHandler stands in for a redacting wrapper: it sees every record
// before passing it on.
type recordingHandler struct {
	next slog.Handler
	tag  string
	log  *recordLog
}

func recordingWrapper(tag string, records *recordLog) func(slog.Handler) slog.Handler {
	return func(next slog.Handler) slog.Handler {
		return &recordingHandler{next: next, tag: tag, log: records}
	}
}

func (h *recordingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *recordingHandler) Handle(ctx context.Context, record slog.Record) error {
	h.log.add(h.tag + ":" + record.Message)
	return h.next.Handle(ctx, record)
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{next: h.next.WithAttrs(attrs), tag: h.tag, log: h.log}
}

func (h *recordingHandler) WithGroup(name string) slog.Handler {
	return &recordingHandler{next: h.next.WithGroup(name), tag: h.tag, log: h.log}
}

// restoreDefaultLogger puts back the default logger (and the log package
// output slog.SetDefault redirects) when the test ends.
func restoreDefaultLogger(t *testing.T) {
	t.Helper()
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
}

func TestConfigureLoggingWithoutWrappersInstallsCoreHandler(t *testing.T) {
	restoreDefaultLogger(t)
	t.Setenv(envLogFormat, "json")
	t.Setenv(envLogLevel, "")

	require.NoError(t, configureLogging(io.Discard, nil))
	_, isJSON := slog.Default().Handler().(*slog.JSONHandler)
	assert.True(t, isJSON)

	core := slog.NewJSONHandler(io.Discard, nil)
	got, err := wrapLogHandler(core, nil)
	require.NoError(t, err)
	assert.Same(t, core, got)
}

func TestConfigureLoggingAppliesWrappersInRegistrationOrder(t *testing.T) {
	restoreDefaultLogger(t)
	t.Setenv(envLogFormat, "json")
	t.Setenv(envLogLevel, "info")

	records := &recordLog{}
	reg := &ext.Registry{}
	reg.WrapLogHandler(recordingWrapper("first", records))
	reg.WrapLogHandler(recordingWrapper("second", records))

	var out bytes.Buffer
	require.NoError(t, configureLogging(&out, reg.LogHandlerWrappers()))

	outer, ok := slog.Default().Handler().(*recordingHandler)
	require.True(t, ok)
	assert.Equal(t, "second", outer.tag)
	inner, ok := outer.next.(*recordingHandler)
	require.True(t, ok)
	assert.Equal(t, "first", inner.tag)
	_, isJSON := inner.next.(*slog.JSONHandler)
	assert.True(t, isJSON)

	slog.Debug("below the configured level")
	slog.Warn("direct")
	slog.Default().With("component", "test").WithGroup("g").Warn("derived")
	log.Print("standard library")

	assert.Equal(t, []string{
		"second:direct", "first:direct",
		"second:derived", "first:derived",
		"second:standard library", "first:standard library",
	}, records.snapshot())
	assert.Contains(t, out.String(), `"msg":"derived"`)
	assert.Contains(t, out.String(), `"component":"test"`)
	assert.NotContains(t, out.String(), "below the configured level")
}

func TestConfigureLoggingRejectsANilHandler(t *testing.T) {
	restoreDefaultLogger(t)
	before := slog.Default()

	err := configureLogging(io.Discard, []func(slog.Handler) slog.Handler{
		func(slog.Handler) slog.Handler { return nil },
	})
	require.Error(t, err)
	assert.Same(t, before, slog.Default())
}

// The wrappers registered before Run cover the logger of every generation,
// including what a reload logs while it loads the configuration, before any
// distribution hook could have wrapped the logger itself.
func TestRunWrapsTheLoggerOfEveryGeneration(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("server:\n  master_key: ${vault:master}\n"), 0o600))
	t.Setenv("GOMODEL_MASTER_KEY", "")
	t.Setenv("PORT", "0")
	t.Setenv("GOMODEL_OFFLINE", "true")
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(dir, "gomodel.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv(envLogLevel, "info")

	records := &recordLog{}
	var wraps atomic.Int32
	reg := &ext.Registry{}
	reg.WrapLogHandler(func(next slog.Handler) slog.Handler {
		wraps.Add(1)
		return recordingWrapper("wrapped", records)(next)
	})

	var master atomic.Value
	master.Store("m1")
	vault := config.SecretResolverFunc(func(context.Context, string) (string, error) {
		return master.Load().(string), nil
	})

	// What each generation's hook observed when it ran.
	type observed struct {
		secrets     *config.Secrets
		wrapped     bool
		configLoads int
	}
	observe := func(result *config.LoadResult) observed {
		_, wrapped := slog.Default().Handler().(*recordingHandler)
		return observed{secrets: result.Secrets, wrapped: wrapped, configLoads: records.count("wrapped:config file loaded")}
	}
	setup := make(chan observed, 1)
	reloads := make(chan observed, 1)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- Run(ctx, Options{
			Args:       []string{},
			Stdout:     io.Discard,
			Stderr:     io.Discard,
			Extensions: reg,
			SetupConfig: func(_ context.Context, result *config.LoadResult) error {
				setup <- observe(result)
				return result.Secrets.Register("vault", vault)
			},
			ReloadConfig: func(_ context.Context, result *config.LoadResult) error {
				reloads <- observe(result)
				return result.Secrets.Register("vault", vault)
			},
		})
	}()

	waitFor := func(ch <-chan observed, what string) observed {
		t.Helper()
		select {
		case got := <-ch:
			return got
		case err := <-served:
			t.Fatalf("Run returned before %s: %v", what, err)
		case <-time.After(30 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
		return observed{}
	}

	first := waitFor(setup, "the first generation")
	assert.True(t, first.wrapped)
	assert.Equal(t, 1, first.configLoads)
	assert.Equal(t, 1, records.count("wrapped:starting gomodel"))

	// A rotated master key reloads the generation.
	master.Store("m2")
	first.secrets.NotifyChanged()
	reloaded := waitFor(reloads, "the reload")
	assert.True(t, reloaded.wrapped)
	assert.Equal(t, 2, reloaded.configLoads, "the reload's config load was not logged through the wrapper")
	assert.Equal(t, int32(2), wraps.Load())

	require.Eventually(t, func() bool {
		return records.count("wrapped:configuration reloaded") == 1
	}, 30*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// configureLogging is where extension log handler wrappers are applied, so a
// slog handler built anywhere else in the gateway would log around them.
func TestSlogHandlersAreBuiltOnlyByConfigureLogging(t *testing.T) {
	constructors := map[string]bool{
		"slog.NewJSONHandler": true,
		"slog.NewTextHandler": true,
		"tint.NewTextHandler": true,
	}
	allowed := map[string]bool{
		"run/logging.go":               true,
		"pluginapi/plugintest/host.go": true, // a discarding logger for plugin unit tests
	}
	skipped := map[string]bool{".git": true, ".claude": true, ".worktrees": true, "node_modules": true, "vendor": true, "web": true}

	root := ".."
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipped[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && constructors[pkg.Name+"."+sel.Sel.Name] {
				offenders = append(offenders, rel+": "+pkg.Name+"."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "build slog handlers through configureLogging so extension wrappers apply:\n%s", strings.Join(offenders, "\n"))
}
