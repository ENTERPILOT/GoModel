package run

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/egress"
	"github.com/enterpilot/gomodel/internal/app"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// The hosts a default install contacts without being told to: the model
// catalog and the release manifest. docs/guides/air-gapped.mdx names both, so
// a new one belongs in that page and under GOMODEL_OFFLINE before it belongs
// in this list.
const (
	modelCatalogHost = "raw.githubusercontent.com:443"
	releaseHost      = "gomodel.enterpilot.io:443"
)

const (
	egressProbeEnv    = "GOMODEL_TEST_EGRESS_PROBE"
	egressProbeMarker = "EGRESS_PROBE_ADDR="
)

// TestOutboundCallsTheGatewayMakesOnItsOwn boots the whole gateway next to a
// local model server, with every other destination reachable only through a
// proxy that records the attempt and refuses it. A default install may try
// the two documented hosts, and has to keep serving when both fail; an
// offline one may try nothing at all.
//
// The gateway runs in a child process because net/http reads the proxy
// variables once per process.
func TestOutboundCallsTheGatewayMakesOnItsOwn(t *testing.T) {
	tests := []struct {
		name               string
		env                []string
		want               []string
		wantVersionEnabled bool
		// wantLogs are startup lines that settle the question without
		// timing: the gateway logs them instead of starting the call.
		wantLogs []string
	}{
		{
			name:               "a default install tries the model catalog and the release host",
			want:               []string{releaseHost, modelCatalogHost},
			wantVersionEnabled: true,
		},
		{
			name: "offline mode tries nothing",
			env:  []string{"GOMODEL_OFFLINE=true"},
			wantLogs: []string{
				// The line docs/guides/air-gapped.mdx tells operators to look for.
				"offline mode: update check, remote model catalog download, and allowlist metadata listing disabled",
				"model list downloads disabled",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := newRefusingProxy(t)
			upstream, calls := providertest.RouteServer(t, map[string]http.HandlerFunc{
				"/v1/models": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"local-model","object":"model","owned_by":"local"}]}`)
				},
				"/v1/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"local-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
				},
			})

			gateway := startEgressProbe(t, append([]string{
				"VLLM_BASE_URL=" + upstream.URL + "/v1",
				"HTTP_PROXY=" + proxy.url,
				"HTTPS_PROXY=" + proxy.url,
			}, tt.env...))

			// Readiness answers 200 before the provider's models are loaded,
			// so wait for the models component, not only for the status.
			require.Eventually(t, func() bool {
				status, body := gateway.get("/health/ready")
				return status == http.StatusOK && strings.Contains(body, `"models":"ok"`)
			}, 30*time.Second, 20*time.Millisecond, "the gateway never loaded the local provider's models")

			// A dashboard's first visit of the day is what asks for an update
			// check right away; the schedule waits out a startup delay first.
			status, body := gateway.get("/version")
			require.Equal(t, http.StatusOK, status)
			assert.Contains(t, body, fmt.Sprintf(`"enabled":%t`, tt.wantVersionEnabled))

			status, body = gateway.post("/v1/chat/completions", `{"model":"local-model","messages":[{"role":"user","content":"hi"}]}`)
			require.Equal(t, http.StatusOK, status, body)
			assert.Equal(t, "/v1/chat/completions", calls.Last(t).Path)

			if len(tt.want) > 0 {
				require.Eventually(t, func() bool {
					return len(proxy.hosts()) == len(tt.want)
				}, 10*time.Second, 20*time.Millisecond, "saw only %v", proxy.hosts())
			}
			gateway.stop(t)
			assert.ElementsMatch(t, tt.want, proxy.hosts(), "hosts the gateway tried beyond the configured provider")
			for _, line := range tt.wantLogs {
				assert.Contains(t, gateway.output.String(), line)
			}
		})
	}
}

// TestEgressProbeGateway is the child process of the test above: it serves a
// gateway built the way Run builds one until its stdin closes. It also
// refuses every connection core opens directly (database, cache, and vector
// store clients, which bypass the proxy variables) and fails if there was
// one.
func TestEgressProbeGateway(t *testing.T) {
	if os.Getenv(egressProbeEnv) == "" {
		t.Skip("child process of TestOutboundCallsTheGatewayMakesOnItsOwn")
	}

	var mu sync.Mutex
	var dialed []string
	hook, err := egress.Install(func(_ context.Context, _, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		mu.Unlock()
		return nil, errors.New("egress probe: direct connections are refused")
	})
	require.NoError(t, err)
	defer hook.Uninstall()

	ctx := t.Context()

	result, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, result.ResolveSecrets(ctx))
	application, err := app.New(ctx, app.Config{
		AppConfig: result,
		Factory:   defaultProviderFactory(result.Config),
	})
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- application.StartWithListener(ctx, listener) }()

	_, err = os.Stdout.WriteString(egressProbeMarker + listener.Addr().String() + "\n")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, os.Stdin)

	require.NoError(t, application.Shutdown(ctx))
	require.NoError(t, <-served)

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, dialed, "the gateway opened direct connections")
}

// refusingProxy is a forward proxy that reaches nothing: it records the host
// of every request it is asked to relay and answers 502.
type refusingProxy struct {
	url  string
	mu   sync.Mutex
	seen []string
}

func newRefusingProxy(t *testing.T) *refusingProxy {
	t.Helper()
	proxy := &refusingProxy{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CONNECT carries the target in the request line; a plain-HTTP
		// request through a proxy carries it in the absolute URL.
		host := r.Host
		if r.Method != http.MethodConnect && r.URL.Host != "" {
			host = r.URL.Host
		}
		proxy.mu.Lock()
		if !slices.Contains(proxy.seen, host) {
			proxy.seen = append(proxy.seen, host)
		}
		proxy.mu.Unlock()
		http.Error(w, "egress probe: no route", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	proxy.url = server.URL
	return proxy
}

// hosts returns each host the proxy was asked for, once.
func (p *refusingProxy) hosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.seen)
}

// egressProbe is a gateway running in a child process.
type egressProbe struct {
	url   string
	cmd   *exec.Cmd
	stdin io.Closer
	// exited is closed once the child's stdout reaches EOF.
	exited <-chan struct{}
	// output is the child's log and test output so far.
	output *lockedBuffer
	client *http.Client
}

// startEgressProbe runs TestEgressProbeGateway in a child process and waits
// for its address. The child gets env and nothing of the caller's
// environment, so a developer's provider keys or proxy settings cannot reach
// it, and keeps all of its state under a temporary directory.
func startEgressProbe(t *testing.T, env []string) *egressProbe {
	t.Helper()
	home := t.TempDir()
	workdir := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.Command(self, "-test.run=^TestEgressProbeGateway$", "-test.v")
	cmd.Dir = workdir
	cmd.Env = append([]string{
		egressProbeEnv + "=1",
		"HOME=" + home,
		"TMPDIR=" + home,
		"STORAGE_TYPE=sqlite",
		"SQLITE_PATH=" + filepath.Join(workdir, "gomodel.db"),
	}, env...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	output := &lockedBuffer{}
	cmd.Stderr = output
	require.NoError(t, cmd.Start())

	// The child's stdout carries its address and, like any test binary, its
	// own test log; everything but the address is kept for a failure report.
	addr := make(chan string, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			if found, ok := strings.CutPrefix(lines.Text(), egressProbeMarker); ok {
				addr <- found
				continue
			}
			_, _ = output.Write(append(lines.Bytes(), '\n'))
		}
	}()

	probe := &egressProbe{
		cmd:    cmd,
		stdin:  stdin,
		exited: exited,
		output: output,
		// Never through the proxy the child was given, or any other.
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{}},
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
		_ = cmd.Wait()
		probe.client.CloseIdleConnections()
		if t.Failed() {
			t.Logf("gateway output:\n%s", output.String())
		}
	})

	select {
	case found := <-addr:
		probe.url = "http://" + found
	case <-exited:
		require.FailNow(t, "the gateway exited before it listened")
	case <-time.After(60 * time.Second):
		require.FailNow(t, "the gateway did not listen in time")
	}
	return probe
}

// lockedBuffer collects the child's stderr and stdout, which are written from
// two goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (p *egressProbe) get(path string) (int, string) {
	resp, err := p.client.Get(p.url + path)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (p *egressProbe) post(path, body string) (int, string) {
	resp, err := p.client.Post(p.url+path, "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// stop closes the child's stdin, which is its cue to shut down, and requires
// a clean exit: the child fails when it saw a direct connection.
func (p *egressProbe) stop(t *testing.T) {
	t.Helper()
	require.NoError(t, p.stdin.Close())
	select {
	case <-p.exited:
	case <-time.After(60 * time.Second):
		require.FailNow(t, "the gateway did not shut down in time")
	}
	require.NoError(t, p.cmd.Wait(), "the gateway process failed")
}
