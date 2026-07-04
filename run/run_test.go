package run

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestRunVersionSkipsSetup(t *testing.T) {
	setupCalled := false
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
	})
	if err != nil {
		t.Fatalf("Run(--version) error = %v", err)
	}
	if setupCalled {
		t.Error("Setup must not run for --version")
	}
	if !strings.HasPrefix(stdout.String(), "gomodel-test ") {
		t.Errorf("version output = %q, want prefix %q", stdout.String(), "gomodel-test ")
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
