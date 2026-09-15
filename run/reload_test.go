package run

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A .env value applies only where the real environment has nothing to say,
// which is godotenv.Load's rule and therefore the rule a reload has to keep.
func TestDotenvLeavesExportedVariablesAlone(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GOMODEL_TEST_EXPORTED", "from-environment")
	writeEnvFile(t, "GOMODEL_TEST_EXPORTED=from-file\nGOMODEL_TEST_FILE_ONLY=from-file\n")
	t.Cleanup(func() { os.Unsetenv("GOMODEL_TEST_FILE_ONLY") })

	newDotenv().apply()
	got := os.Getenv("GOMODEL_TEST_EXPORTED")
	assert.Equal(t, "from-environment", got)
	got = os.Getenv("GOMODEL_TEST_FILE_ONLY")
	assert.Equal(t, "from-file", got)
}

// Reloading is worth little if it cannot see edited credentials and endpoints,
// so a second apply must pick up new values and forget deleted ones — without
// ever taking over a variable the process was started with.
func TestDotenvReappliesEditedFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GOMODEL_TEST_EXPORTED", "from-environment")
	writeEnvFile(t, "GOMODEL_TEST_EXPORTED=from-file\nGOMODEL_TEST_EDITED=before\nGOMODEL_TEST_REMOVED=present\n")
	t.Cleanup(func() {
		os.Unsetenv("GOMODEL_TEST_EDITED")
		os.Unsetenv("GOMODEL_TEST_REMOVED")
	})

	env := newDotenv()
	env.apply()
	writeEnvFile(t, "GOMODEL_TEST_EXPORTED=from-file\nGOMODEL_TEST_EDITED=after\n")
	env.apply()
	got := os.Getenv("GOMODEL_TEST_EDITED")
	assert.Equal(t, "after", got)
	_, present := os.LookupEnv("GOMODEL_TEST_REMOVED")
	assert.False(t, present)
	got = os.Getenv("GOMODEL_TEST_EXPORTED")
	assert.Equal(t, "from-environment", got)
}

// A missing .env file is the normal case for container deployments: it means
// "configuration comes from the environment", not "keep the last file I saw".
func TestDotenvClearsWhenTheFileDisappears(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeEnvFile(t, "GOMODEL_TEST_VANISHING=present\n")
	t.Cleanup(func() { os.Unsetenv("GOMODEL_TEST_VANISHING") })

	env := newDotenv()
	env.apply()
	err := os.Remove(filepath.Join(dir, envFile))
	require.NoError(t, err)

	env.apply()
	_, present := os.LookupEnv("GOMODEL_TEST_VANISHING")
	assert.False(t, present)
}

func TestPIDFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "gomodel.pid")

	remove, err := writePIDFile(path)
	require.NoError(t, err)

	pid, err := readPIDFile(path)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), pid)

	remove()
	_, err = readPIDFile(path)
	assert.Error(t, err)
}

func TestPIDFileEmptyPathIsANoop(t *testing.T) {
	remove, err := writePIDFile("  ")
	require.NoError(t, err)

	remove()
}

func TestReadPIDFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gomodel.pid")
	err := os.WriteFile(path, []byte("not-a-pid"), 0o644)
	require.NoError(t, err)
	_, err = readPIDFile(path)
	assert.Error(t, err)
}

// The whole point of building the replacement before stopping what is running:
// a configuration that does not load must cost nothing but a log line.
func TestServeUntilShutdownKeepsServingWhenReloadFails(t *testing.T) {
	socket := testSocket(t)
	first := newFakeGeneration()
	second := newFakeGeneration()

	attempts := make(chan struct{}, 2)
	var count atomic.Int32
	rebuild := func() (lifecycleApp, error) {
		defer func() { attempts <- struct{}{} }()
		if count.Add(1) == 1 {
			return nil, errors.New("invalid configuration")
		}
		return second, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan os.Signal, 1)
	served := make(chan error, 1)
	go func() { served <- serveUntilShutdown(ctx, reload, socket, first, rebuild) }()

	<-first.started
	reload <- reloadSignal
	<-attempts
	require.Equal(t, int32(0), first.shutdowns.Load())

	reload <- reloadSignal
	<-attempts
	<-second.started
	got := first.shutdowns.Load()
	require.Equal(t, int32(1), got)

	cancel()
	select {
	case err := <-served:
		require.NoError(t, err)

	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilShutdown did not return after cancellation")
	}
	got = second.shutdowns.Load()
	require.Equal(t, int32(1), got)
}

// Reloading must not cost the port. This walks the window a reload opens: the
// generation that was serving has closed its listener and the next one has not
// started, and a client connecting right then must still be connected — waiting
// in the kernel's accept queue — rather than refused.
func TestBoundSocketSurvivesGenerations(t *testing.T) {
	socket := testSocket(t)
	if socket.file == nil {
		t.Skip("this platform cannot duplicate the listening socket; generations rebind instead")
	}

	first, err := socket.next()
	require.NoError(t, err)

	address := first.Addr().String()
	err = first.Close()
	require.NoError(t, err)

	// Nothing is accepting at this point.
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	require.NoError(t, err)

	defer conn.Close()

	second, err := socket.next()
	require.NoError(t, err)

	defer second.Close()
	got := second.Addr().String()
	require.Equal(t, address, got)

	if deadliner, ok := second.(interface{ SetDeadline(time.Time) error }); ok {
		err := deadliner.SetDeadline(time.Now().Add(5 * time.Second))
		require.NoError(t, err)
	}

	waiting, err := second.Accept()
	require.NoError(t, err)

	_ = waiting.Close()
}

func testSocket(t *testing.T) *boundSocket {
	t.Helper()
	socket, err := listenOn("127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = socket.Close() })
	return socket
}

func writeEnvFile(t *testing.T, contents string) {
	t.Helper()
	err := os.WriteFile(envFile, []byte(contents), 0o600)
	require.NoError(t, err)
}

// fakeGeneration stands in for one built application: it serves until it is
// shut down, and records how often that happened.
type fakeGeneration struct {
	started   chan struct{}
	stopped   chan struct{}
	stopOnce  sync.Once
	shutdowns atomic.Int32
}

func newFakeGeneration() *fakeGeneration {
	return &fakeGeneration{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (g *fakeGeneration) StartWithListener(_ context.Context, listener net.Listener) error {
	if listener != nil {
		defer listener.Close()
	}
	close(g.started)
	<-g.stopped
	return nil
}

func (g *fakeGeneration) Shutdown(context.Context) error {
	g.shutdowns.Add(1)
	g.stopOnce.Do(func() { close(g.stopped) })
	return nil
}

// A second instance configured with the same pid file path owns it; the first
// one must not remove it on its way out, or --reload loses the survivor.
func TestPIDFileRemovalLeavesAnotherInstanceAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gomodel.pid")
	remove, err := writePIDFile(path)
	require.NoError(t, err)
	err = os.WriteFile(path, []byte("424242\n"), 0o644)
	require.NoError(t, err)

	remove()

	pid, err := readPIDFile(path)
	require.NoError(t, err)
	assert.Equal(t, 424242, pid)
}

// A reload reads the environment file before it can know whether the
// configuration built from it works. When it does not, the generation that
// keeps serving must keep the environment it was built with — the operator was
// told the new configuration was rejected.
func TestDotenvApplyUndoRestoresTheEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GOMODEL_TEST_EXPORTED", "from-environment")
	writeEnvFile(t, "GOMODEL_TEST_KEPT=before\nGOMODEL_TEST_DROPPED=present\n")
	t.Cleanup(func() {
		os.Unsetenv("GOMODEL_TEST_KEPT")
		os.Unsetenv("GOMODEL_TEST_DROPPED")
		os.Unsetenv("GOMODEL_TEST_ADDED")
	})

	env := newDotenv()
	env.apply()

	// The edit a failed reload would have read.
	writeEnvFile(t, "GOMODEL_TEST_KEPT=after\nGOMODEL_TEST_ADDED=new\nGOMODEL_TEST_EXPORTED=from-file\n")
	undo := env.apply()
	got := os.Getenv("GOMODEL_TEST_KEPT")
	require.Equal(t, "after", got)

	undo()
	got = os.Getenv("GOMODEL_TEST_KEPT")
	assert.Equal(t, "before", got)
	got = os.Getenv("GOMODEL_TEST_DROPPED")
	assert.Equal(t, "present", got)
	_, present := os.LookupEnv("GOMODEL_TEST_ADDED")
	assert.False(t, present)
	got = os.Getenv("GOMODEL_TEST_EXPORTED")
	assert.Equal(t, "from-environment", got)

	// The bookkeeping has to be restored too, or the next reload treats the
	// rolled-back variables as none of its business.
	writeEnvFile(t, "GOMODEL_TEST_KEPT=third\n")
	env.apply()
	got = os.Getenv("GOMODEL_TEST_KEPT")
	assert.Equal(t, "third", got)
	_, present = os.LookupEnv("GOMODEL_TEST_DROPPED")
	assert.False(t, present)
}

func TestSendReloadSignal(t *testing.T) {
	tests := []struct {
		name      string
		pidFile   func(t *testing.T, dir string) string
		wantError bool
	}{
		{
			name: "signals the process named by the pid file",
			pidFile: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "gomodel.pid")
				remove, err := writePIDFile(path)
				require.NoError(t, err)

				t.Cleanup(remove)
				return path
			},
		},
		{
			name: "reports a missing pid file",
			pidFile: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "absent.pid")
			},
			wantError: true,
		},
		{
			name: "reports a pid file that names no process",
			pidFile: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "garbage.pid")
				err := os.WriteFile(path, []byte("not-a-pid"), 0o644)
				require.NoError(t, err)

				return path
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir) // no config.yaml here, so only PID_FILE decides the path
			t.Setenv("PID_FILE", tt.pidFile(t, dir))

			// Registered before signalling, exactly as the gateway does it, so a
			// delivered SIGHUP is caught here instead of killing the test binary.
			delivered := make(chan os.Signal, 1)
			signal.Notify(delivered, reloadSignal)
			defer signal.Stop(delivered)

			var out strings.Builder
			err := sendReloadSignal(&out)
			if tt.wantError {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)

			select {
			case <-delivered:
			case <-time.After(5 * time.Second):
				t.Fatal("the reload signal was never delivered")
			}
			assert.Contains(t, out.String(), "reload requested")

		})
	}
}
