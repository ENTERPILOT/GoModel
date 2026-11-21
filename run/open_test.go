package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCLI_AcceptsSingleAndDoubleDashOpen(t *testing.T) {
	for _, args := range [][]string{{"-open"}, {"--open"}} {
		opts, err := parseCLI("gomodel", args, io.Discard)
		require.NoError(t, err)
		require.True(t, opts.Open, "parseCLI(%v).Open = false, want true", args)
	}
}

func TestBrowserCommand(t *testing.T) {
	const target = "http://localhost:8080/admin/dashboard"
	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		want    []string
		wantErr string
	}{
		{name: "macOS", goos: "darwin", want: []string{"open", target}},
		{name: "Windows", goos: "windows", want: []string{"rundll32", "url.dll,FileProtocolHandler", target}},
		{name: "Linux on X11", goos: "linux", env: map[string]string{"DISPLAY": ":0"}, want: []string{"xdg-open", target}},
		{name: "Linux on Wayland", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, want: []string{"xdg-open", target}},
		{name: "FreeBSD desktop", goos: "freebsd", env: map[string]string{"DISPLAY": ":0"}, want: []string{"xdg-open", target}},
		{name: "Linux without a graphical session", goos: "linux", wantErr: "no graphical session"},
		{name: "unsupported OS", goos: "plan9", wantErr: "not supported on plan9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := browserCommand(tt.goos, func(key string) string { return tt.env[key] }, target)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// stubBrowser replaces the browser launcher for the test and returns the
// addresses it was asked to open.
func stubBrowser(t *testing.T, err error) <-chan string {
	t.Helper()
	opened := make(chan string, 4)
	previous := launchBrowser
	launchBrowser = func(target string) error {
		opened <- target
		return err
	}
	t.Cleanup(func() { launchBrowser = previous })
	return opened
}

func TestOpenDashboard(t *testing.T) {
	const target = "http://localhost:8080/admin/dashboard"
	tests := []struct {
		name       string
		target     string
		launchErr  error
		wantOpened bool
		wantLog    string
	}{
		{name: "opens the dashboard", target: target, wantOpened: true, wantLog: "opening the dashboard in the default browser"},
		{name: "a failed launch names the address to open by hand", target: target, launchErr: errors.New("exec: \"xdg-open\": executable file not found in $PATH"), wantOpened: true, wantLog: "could not open the dashboard in a browser; open it manually"},
		{name: "no dashboard, nothing to open", target: "", wantLog: "--open ignored"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreDefaultLogger(t)
			t.Setenv(envLogFormat, "text")
			t.Setenv(envLogLevel, "info")
			var logs bytes.Buffer
			require.NoError(t, configureLogging(&logs, nil))
			opened := stubBrowser(t, tt.launchErr)

			openDashboard(tt.target)

			assert.Contains(t, logs.String(), tt.wantLog)
			if !tt.wantOpened {
				assert.Empty(t, opened)
				return
			}
			require.Len(t, opened, 1)
			assert.Equal(t, tt.target, <-opened)
			assert.Contains(t, logs.String(), tt.target)
		})
	}
}

// startGateway runs the gateway with args in an empty working directory until
// the test ends, and returns its logs and the channel Run's result arrives on.
func startGateway(t *testing.T, args []string, env map[string]string) (*syncBuffer, <-chan error) {
	t.Helper()
	restoreDefaultLogger(t)
	dir := t.TempDir()
	t.Chdir(dir)
	// A local data directory keeps the pid file and the install id out of the
	// per-user data directory of whoever runs the tests.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "data"), 0o750))
	t.Setenv("GOMODEL_MASTER_KEY", "")
	t.Setenv("GOMODEL_OFFLINE", "true")
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(dir, "data", "gomodel.db"))
	t.Setenv(envLogFormat, "text")
	t.Setenv(envLogLevel, "info")
	for key, value := range env {
		t.Setenv(key, value)
	}

	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	stopped := make(chan struct{})
	// Registered after the environment, working directory, and logger
	// cleanups, so it runs first: Run has stopped before they are restored.
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Error("Run did not stop")
		}
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	go func() {
		defer close(stopped)
		served <- Run(ctx, Options{Args: args, Stdout: logs, Stderr: logs})
	}()
	return logs, served
}

// --open hands the browser the dashboard address of the gateway it started,
// and that address answers. PORT=0 lets the operating system choose the port,
// so the address has to carry the bound port rather than the configured one.
func TestRunOpenOpensTheServedDashboard(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
	}{
		{name: "at the root"},
		{name: "under a base path", basePath: "/gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := stubBrowser(t, nil)
			_, served := startGateway(t, []string{"--open"}, map[string]string{"PORT": "0", "BASE_PATH": tt.basePath})

			var target string
			select {
			case target = <-opened:
			case err := <-served:
				t.Fatalf("Run returned before opening the dashboard: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("timed out waiting for the dashboard to be opened")
			}
			u, err := url.Parse(target)
			require.NoError(t, err)
			assert.Equal(t, "http", u.Scheme)
			assert.Equal(t, "localhost", u.Hostname())
			assert.NotEqual(t, "0", u.Port())
			assert.Equal(t, tt.basePath+"/admin/dashboard", u.Path)

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Get(target)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Empty(t, opened, "the dashboard is opened once")
		})
	}
}

func TestRunOpenWithoutADashboardStillServes(t *testing.T) {
	opened := stubBrowser(t, nil)
	logs, served := startGateway(t, []string{"--open"}, map[string]string{"PORT": "0", "ADMIN_UI_ENABLED": "false"})

	// The line carries the bound address, such as address=[::]:54321.
	boundPort := regexp.MustCompile(`starting server.*address=\S*:(\d+)`)
	var port string
	require.Eventually(t, func() bool {
		select {
		case err := <-served:
			t.Errorf("Run returned before serving: %v", err)
			return true
		default:
		}
		match := boundPort.FindStringSubmatch(logs.String())
		if match == nil {
			return false
		}
		port = match[1]
		return true
	}, 30*time.Second, 20*time.Millisecond, "never logged \"starting server\"")
	require.NotEmpty(t, port)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("http://localhost:" + port + "/health")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, logs.String(), "--open ignored")
	assert.Empty(t, opened)
}

func TestWithBoundPort(t *testing.T) {
	bound := &net.TCPAddr{IP: net.IPv6unspecified, Port: 54321}
	tests := []struct {
		name   string
		target string
		bound  net.Addr
		want   string
	}{
		{name: "port chosen by the operating system", target: "http://localhost:0/admin/dashboard", bound: bound, want: "http://localhost:54321/admin/dashboard"},
		{name: "configured port", target: "https://localhost:54321/gateway/admin/dashboard", bound: bound, want: "https://localhost:54321/gateway/admin/dashboard"},
		{name: "no dashboard", target: "", bound: bound, want: ""},
		{name: "not a TCP socket", target: "http://localhost:8080/admin/dashboard", bound: &net.UnixAddr{Name: "gomodel.sock", Net: "unix"}, want: "http://localhost:8080/admin/dashboard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, withBoundPort(tt.target, tt.bound))
		})
	}
}
