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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	assert.False(t, setupCalled)
	assert.False(t, setupConfigCalled)
	assert.True(t, strings.HasPrefix(stdout.String(), "gomodel-test "), "version output = %q, want prefix %q", stdout.String(), "gomodel-test ")
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
		require.ErrorIs(t, err, wantErr, "generation %d", i)
	}
	require.Equal(t, 1, setups)
	require.Equal(t, 2, reloads)

	// Without hooks every generation passes.
	noHooks := configHooks(t.Context(), Options{})
	for generation := 1; generation <= 2; generation++ {
		err := noHooks(result)
		require.NoError(t, err, "no hooks, generation %d: %v", generation, err)
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
			require.NotNil(t, result)
			require.NotNil(t, result.Config)

			return wantErr
		},
	})
	require.True(t, called)
	require.ErrorIs(t, err, wantErr)
}

func TestRunUsageErrorExitCode(t *testing.T) {
	err := Run(context.Background(), Options{
		Args:   []string{"--not-a-flag"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	require.Error(t, err)
	got := ExitCode(err)
	assert.Equal(t, 2, got)
}

func TestRunHelpIsNotAnError(t *testing.T) {
	err := Run(context.Background(), Options{
		Args:   []string{"--help"},
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	require.NoError(t, err)
	got := ExitCode(err)
	assert.Equal(t, 0, got)
}

// TestRunHealthAndReadyDispatch exercises the --health/--ready short-circuit
// paths end-to-end: Run must probe the locally configured gateway port and
// surface probe failures as non-usage errors (exit code 1).
func TestRunHealthAndReadyDispatch(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

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
		err := Run(context.Background(), Options{
			Args:   []string{flag},
			Stdout: io.Discard,
			Stderr: io.Discard,
		})
		require.NoError(t, err, "Run(%s) against healthy gateway = %v, want nil", flag, err)
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
		got := ExitCode(err)
		assert.Equal(t, 1, got, "ExitCode(Run(%s) error) = %d, want 1", flag, got)
	}
}
