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
)

// A .env value applies only where the real environment has nothing to say,
// which is godotenv.Load's rule and therefore the rule a reload has to keep.
func TestDotenvLeavesExportedVariablesAlone(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GOMODEL_TEST_EXPORTED", "from-environment")
	writeEnvFile(t, "GOMODEL_TEST_EXPORTED=from-file\nGOMODEL_TEST_FILE_ONLY=from-file\n")
	t.Cleanup(func() { os.Unsetenv("GOMODEL_TEST_FILE_ONLY") })

	newDotenv().apply()

	if got := os.Getenv("GOMODEL_TEST_EXPORTED"); got != "from-environment" {
		t.Errorf("exported variable = %q, want it untouched by the env file", got)
	}
	if got := os.Getenv("GOMODEL_TEST_FILE_ONLY"); got != "from-file" {
		t.Errorf("file-only variable = %q, want %q", got, "from-file")
	}
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

	if got := os.Getenv("GOMODEL_TEST_EDITED"); got != "after" {
		t.Errorf("edited variable = %q, want %q", got, "after")
	}
	if _, present := os.LookupEnv("GOMODEL_TEST_REMOVED"); present {
		t.Error("variable dropped from the env file is still set")
	}
	if got := os.Getenv("GOMODEL_TEST_EXPORTED"); got != "from-environment" {
		t.Errorf("exported variable = %q, want it untouched by the env file", got)
	}
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
	if err := os.Remove(filepath.Join(dir, envFile)); err != nil {
		t.Fatal(err)
	}
	env.apply()

	if _, present := os.LookupEnv("GOMODEL_TEST_VANISHING"); present {
		t.Error("variable survived the removal of the env file")
	}
}

func TestPIDFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "gomodel.pid")

	remove, err := writePIDFile(path)
	if err != nil {
		t.Fatalf("writePIDFile() error = %v", err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile() error = %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}

	remove()
	if _, err := readPIDFile(path); err == nil {
		t.Error("readPIDFile() after removal = nil error, want an error")
	}
}

func TestPIDFileEmptyPathIsANoop(t *testing.T) {
	remove, err := writePIDFile("  ")
	if err != nil {
		t.Fatalf("writePIDFile(\"\") error = %v", err)
	}
	remove()
}

func TestReadPIDFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gomodel.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPIDFile(path); err == nil {
		t.Error("readPIDFile() on a garbage file = nil error, want an error")
	}
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
	if first.shutdowns.Load() != 0 {
		t.Fatal("a failed reload stopped the running generation")
	}

	reload <- reloadSignal
	<-attempts
	<-second.started
	if got := first.shutdowns.Load(); got != 1 {
		t.Fatalf("first generation shutdowns = %d, want 1", got)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serveUntilShutdown() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilShutdown did not return after cancellation")
	}
	if got := second.shutdowns.Load(); got != 1 {
		t.Fatalf("second generation shutdowns = %d, want 1", got)
	}
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
	if err != nil {
		t.Fatalf("socket.next() error = %v", err)
	}
	address := first.Addr().String()
	if err := first.Close(); err != nil {
		t.Fatalf("close first listener: %v", err)
	}

	// Nothing is accepting at this point.
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("connect while no generation is accepting: %v", err)
	}
	defer conn.Close()

	second, err := socket.next()
	if err != nil {
		t.Fatalf("socket.next() after a generation ended = %v", err)
	}
	defer second.Close()
	if got := second.Addr().String(); got != address {
		t.Fatalf("second generation address = %q, want %q", got, address)
	}
	if deadliner, ok := second.(interface{ SetDeadline(time.Time) error }); ok {
		if err := deadliner.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set accept deadline: %v", err)
		}
	}

	waiting, err := second.Accept()
	if err != nil {
		t.Fatalf("accept the connection that waited through the swap: %v", err)
	}
	_ = waiting.Close()
}

func testSocket(t *testing.T) *boundSocket {
	t.Helper()
	socket, err := listenOn("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenOn() error = %v", err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	return socket
}

func writeEnvFile(t *testing.T, contents string) {
	t.Helper()
	if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatalf("writePIDFile() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("424242\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	remove()

	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile() error = %v, want the other instance's pid file intact", err)
	}
	if pid != 424242 {
		t.Errorf("pid = %d, want 424242", pid)
	}
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
	if got := os.Getenv("GOMODEL_TEST_KEPT"); got != "after" {
		t.Fatalf("edited variable before undo = %q, want %q", got, "after")
	}

	undo()

	if got := os.Getenv("GOMODEL_TEST_KEPT"); got != "before" {
		t.Errorf("edited variable after undo = %q, want %q", got, "before")
	}
	if got := os.Getenv("GOMODEL_TEST_DROPPED"); got != "present" {
		t.Errorf("removed variable after undo = %q, want %q", got, "present")
	}
	if _, present := os.LookupEnv("GOMODEL_TEST_ADDED"); present {
		t.Error("variable added by the rejected file is still set")
	}
	if got := os.Getenv("GOMODEL_TEST_EXPORTED"); got != "from-environment" {
		t.Errorf("exported variable = %q, want it untouched throughout", got)
	}

	// The bookkeeping has to be restored too, or the next reload treats the
	// rolled-back variables as none of its business.
	writeEnvFile(t, "GOMODEL_TEST_KEPT=third\n")
	env.apply()
	if got := os.Getenv("GOMODEL_TEST_KEPT"); got != "third" {
		t.Errorf("variable after a later reload = %q, want %q", got, "third")
	}
	if _, present := os.LookupEnv("GOMODEL_TEST_DROPPED"); present {
		t.Error("variable dropped from the env file survived the later reload")
	}
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
				if err != nil {
					t.Fatalf("writePIDFile() error = %v", err)
				}
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
				if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
					t.Fatal(err)
				}
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
				if err == nil {
					t.Fatal("sendReloadSignal() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("sendReloadSignal() error = %v", err)
			}

			select {
			case <-delivered:
			case <-time.After(5 * time.Second):
				t.Fatal("the reload signal was never delivered")
			}
			if !strings.Contains(out.String(), "reload requested") {
				t.Errorf("output = %q, want it to confirm the reload", out.String())
			}
		})
	}
}
