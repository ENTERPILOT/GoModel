package run

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
)

func TestRunVersionSkipsSetup(t *testing.T) {
	setupCalled := false
	setupConfigCalled := false
	var stdout strings.Builder

	err := Run(context.Background(), Options{
		ProductName: "gomodel-test",
		Args:        []string{"--version"},
		Stdout:      &stdout,
		Stderr:      io.Discard,
		Setup: func(context.Context) error {
			setupCalled = true
			return nil
		},
		SetupConfig: func(context.Context, *config.LoadResult) error {
			setupConfigCalled = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run(--version) error = %v", err)
	}
	if setupCalled {
		t.Error("Setup must not run for --version")
	}
	if setupConfigCalled {
		t.Error("SetupConfig must not run for --version")
	}
	if !strings.HasPrefix(stdout.String(), "gomodel-test ") {
		t.Errorf("version output = %q, want prefix %q", stdout.String(), "gomodel-test ")
	}
}

func TestConfigHooksRunSetupOnceThenReloadForEveryLaterGeneration(t *testing.T) {
	var setups, reloads int
	rejected := errors.New("endpoint outside the policy")
	configure := configHooks(t.Context(), Options{
		SetupConfig: func(context.Context, *config.LoadResult) error {
			setups++
			return nil
		},
		ReloadConfig: func(context.Context, *config.LoadResult) error {
			reloads++
			if reloads == 2 {
				return rejected
			}
			return nil
		},
	})
	result := &config.LoadResult{Config: &config.Config{}}
	for i, wantErr := range []error{nil, nil, rejected} {
		err := configure(result)
		if !errors.Is(err, wantErr) {
			t.Fatalf("generation %d: error = %v, want %v", i+1, err, wantErr)
		}
	}
	if setups != 1 || reloads != 2 {
		t.Fatalf("SetupConfig ran %d times and ReloadConfig %d, want 1 and 2", setups, reloads)
	}
	// Without hooks every generation passes.
	noHooks := configHooks(t.Context(), Options{})
	for generation := 1; generation <= 2; generation++ {
		if err := noHooks(result); err != nil {
			t.Fatalf("no hooks, generation %d: %v", generation, err)
		}
	}
}

func TestRunSetupConfigReceivesLoadedConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	wantErr := errors.New("configured extension stopped startup")
	called := false
	err := Run(t.Context(), Options{
		Args:   []string{},
		Stdout: io.Discard,
		Stderr: io.Discard,
		SetupConfig: func(_ context.Context, result *config.LoadResult) error {
			called = true
			if result == nil || result.Config == nil {
				t.Fatal("SetupConfig received nil configuration")
			}
			return wantErr
		},
	})
	if !called {
		t.Fatal("SetupConfig was not called")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want wrapped sentinel", err)
	}
}

func TestRunUsageErrorExitCode(t *testing.T) {
	err := Run(context.Background(), Options{
		Args:   []string{"--not-a-flag"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("ExitCode = %d, want 2", got)
	}
}

func TestRunHelpIsNotAnError(t *testing.T) {
	err := Run(context.Background(), Options{
		Args:   []string{"--help"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run(--help) error = %v, want nil", err)
	}
	if got := ExitCode(err); got != 0 {
		t.Errorf("ExitCode = %d, want 0", got)
	}
}

// TestRunHealthAndReadyDispatch exercises the --health/--ready short-circuit
// paths end-to-end: Run must probe the locally configured gateway port and
// surface probe failures as non-usage errors (exit code 1).
func TestRunHealthAndReadyDispatch(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/health/ready":
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	t.Setenv("PORT", port)

	for _, flag := range []string{"--health", "--ready"} {
		if err := Run(context.Background(), Options{
			Args:   []string{flag},
			Stdout: io.Discard,
			Stderr: io.Discard,
		}); err != nil {
			t.Errorf("Run(%s) against healthy gateway = %v, want nil", flag, err)
		}
	}

	// An unreachable gateway must surface as a non-usage error (exit code 1).
	_ = srv.Close()
	_ = listener.Close()
	for _, flag := range []string{"--health", "--ready"} {
		err := Run(context.Background(), Options{
			Args:   []string{flag},
			Stdout: io.Discard,
			Stderr: io.Discard,
		})
		if err == nil {
			t.Errorf("Run(%s) against closed port = nil, want error", flag)
			continue
		}
		if got := ExitCode(err); got != 1 {
			t.Errorf("ExitCode(Run(%s) error) = %d, want 1", flag, got)
		}
	}
}
