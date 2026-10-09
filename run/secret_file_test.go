package run

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer safe to write from the gateway's goroutines
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// setupSecretFileGateway points the master key at a file holding contents
// and isolates the gateway in a temporary directory. It returns the key file
// and the port the gateway will listen on.
func setupSecretFileGateway(t *testing.T, contents string) (keyFile, port string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	keyFile = filepath.Join(dir, "master_key")
	require.NoError(t, os.WriteFile(keyFile, []byte(contents), 0o600))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	require.NoError(t, listener.Close())

	t.Setenv("GOMODEL_MASTER_KEY", "${file:"+keyFile+"}")
	t.Setenv("PORT", port)
	t.Setenv("GOMODEL_OFFLINE", "true")
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(dir, "gomodel.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	return keyFile, port
}

// An empty master key file must never start the gateway without
// authentication.
func TestRunRejectsAnEmptyMasterKeyFileAtStartup(t *testing.T) {
	for name, contents := range map[string]string{"empty": "", "newline only": "\n"} {
		t.Run(name, func(t *testing.T) {
			keyFile, _ := setupSecretFileGateway(t, contents)

			// Without the fix the gateway starts and serves until this timeout.
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err := Run(ctx, Options{Args: []string{}, Stdout: io.Discard, Stderr: io.Discard})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to resolve secret references")
			assert.Contains(t, err.Error(), keyFile)
		})
	}
}

// Emptying the master key file and reloading must be rejected: the gateway
// keeps the key it was serving with instead of dropping to unauthenticated
// mode.
func TestRunReloadRejectsAnEmptiedMasterKeyFile(t *testing.T) {
	keyFile, port := setupSecretFileGateway(t, "mk\n")
	logs := &syncBuffer{}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() {
		served <- Run(ctx, Options{Args: []string{}, Stdout: io.Discard, Stderr: logs})
	}()
	// Registered after setupSecretFileGateway's cleanups, so it runs first:
	// the gateway stops before the environment, working directory and temp
	// files it uses are restored, even when a check below fails.
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Error("Run did not stop")
		}
	})

	adminURL := "http://127.0.0.1:" + port + "/admin/provider-credentials"
	status := func(key string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL, nil)
		if err != nil {
			return 0
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	require.Eventually(t, func() bool { return status("mk") == http.StatusOK }, 30*time.Second, 50*time.Millisecond,
		"gateway never served with the master key")
	require.Equal(t, http.StatusUnauthorized, status(""))

	require.NoError(t, os.WriteFile(keyFile, nil, 0o600))
	self, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, self.Signal(reloadSignal))

	const rejected, accepted = "reload failed; keeping the running configuration", "configuration reloaded"
	require.Eventually(t, func() bool {
		out := logs.String()
		return strings.Contains(out, rejected) || strings.Contains(out, accepted)
	}, 30*time.Second, 50*time.Millisecond, "reload never finished")
	out := logs.String()
	require.Contains(t, out, rejected)
	assert.NotContains(t, out, accepted)
	assert.NotContains(t, out, "UNSAFE MODE")
	assert.Contains(t, out, keyFile+" is empty")

	assert.Equal(t, http.StatusUnauthorized, status(""))
	assert.Equal(t, http.StatusOK, status("mk"))
}
