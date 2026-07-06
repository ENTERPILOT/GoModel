package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gomodel/internal/version"
)

// connectTimeout bounds one upstream dial + initialize handshake.
const connectTimeout = 15 * time.Second

// listTimeout bounds one full catalog listing pass.
const listTimeout = 30 * time.Second

// upstream owns the client session and catalog snapshot for one server. One
// shared session serves all downstream sessions: v1 forwards no per-user
// upstream credentials and bridges no server-to-client requests, so
// multiplexing is safe and avoids a session per client (or worse, per call).
type upstream struct {
	spec       ServerSpec
	httpClient *http.Client

	// connectMu serializes dial/refresh so concurrent callers cannot race a
	// reconnect. stateMu guards the fields below and is never held across IO.
	connectMu sync.Mutex
	stateMu   sync.Mutex

	session     *mcp.ClientSession
	catalog     *catalog
	status      ServerStatus
	lastErr     string
	connectedAt time.Time
}

func newUpstream(spec ServerSpec, httpClient *http.Client) *upstream {
	u := &upstream{spec: spec, httpClient: httpClient, status: StatusConnecting}
	if !spec.Enabled {
		u.status = StatusDisabled
	}
	return u
}

// view snapshots the upstream state for admin/dashboard consumption.
func (u *upstream) view() ServerView {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return ServerView{
		Spec:          u.spec,
		Status:        u.status,
		LastError:     u.lastErr,
		ToolCount:     u.catalog.toolCount(),
		PromptCount:   u.catalog.promptCount(),
		ResourceCount: u.catalog.resourceCount(),
		ConnectedAt:   u.connectedAt,
	}
}

// snapshot returns the current catalog (nil when never listed).
func (u *upstream) snapshot() (*catalog, ServerStatus) {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.catalog, u.status
}

// refresh (re)establishes the session if needed and rebuilds the catalog. On
// failure the server is marked degraded and any previous catalog is kept, so
// a transient upstream hiccup does not blank out tools that were working — a
// failed listing must never register an "empty but connected" server.
func (u *upstream) refresh(ctx context.Context) error {
	u.connectMu.Lock()
	defer u.connectMu.Unlock()
	if !u.spec.Enabled {
		return nil
	}

	session, err := u.ensureSessionLocked(ctx)
	if err != nil {
		u.markDegraded(err)
		return err
	}

	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	fresh, err := u.list(listCtx, session)
	if err != nil {
		u.markDegraded(err)
		return err
	}

	u.stateMu.Lock()
	u.catalog = fresh
	u.status = StatusConnected
	u.lastErr = ""
	u.stateMu.Unlock()
	return nil
}

// ensureSessionLocked returns the live session, dialing when necessary.
// connectMu must be held.
func (u *upstream) ensureSessionLocked(ctx context.Context) (*mcp.ClientSession, error) {
	u.stateMu.Lock()
	session := u.session
	u.stateMu.Unlock()
	if session != nil {
		return session, nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	transport, err := u.transport()
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "gomodel",
		Title:   "GoModel MCP Gateway",
		Version: version.Version,
	}, u.clientOptions())
	session, err = client.Connect(dialCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to mcp server %q: %w", u.spec.Name, err)
	}

	u.stateMu.Lock()
	u.session = session
	u.connectedAt = time.Now().UTC()
	u.stateMu.Unlock()
	return session, nil
}

// clientOptions wires upstream change notifications into catalog refreshes.
// No sampling/elicitation/roots handlers are set, so those capabilities are
// not advertised upstream and servers legally never send such requests.
func (u *upstream) clientOptions() *mcp.ClientOptions {
	relist := func() {
		go func() {
			if err := u.refresh(context.Background()); err != nil {
				slog.Warn("mcp catalog refresh after list_changed failed",
					"server", u.spec.Name, "error", err)
			}
		}()
	}
	return &mcp.ClientOptions{
		ToolListChangedHandler:     func(context.Context, *mcp.ToolListChangedRequest) { relist() },
		PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) { relist() },
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { relist() },
	}
}

// transport builds a fresh transport for one dial attempt.
func (u *upstream) transport() (mcp.Transport, error) {
	switch u.spec.Transport {
	case "http", "":
		return &mcp.StreamableClientTransport{
			Endpoint:   u.spec.URL,
			HTTPClient: u.httpClientWithHeaders(),
		}, nil
	case "sse":
		return &mcp.SSEClientTransport{
			Endpoint:   u.spec.URL,
			HTTPClient: u.httpClientWithHeaders(),
		}, nil
	case "stdio":
		cmd := exec.Command(u.spec.Command, u.spec.Args...)
		cmd.Env = os.Environ()
		for key, value := range u.spec.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Stderr = os.Stderr
		return &mcp.CommandTransport{Command: cmd}, nil
	default:
		return nil, fmt.Errorf("mcp server %q: unsupported transport %q", u.spec.Name, u.spec.Transport)
	}
}

// httpClientWithHeaders overlays the configured static headers on the shared
// HTTP client. The headers carry the upstream credential; the client's own
// bearer token was terminated at the gateway and is never forwarded.
func (u *upstream) httpClientWithHeaders() *http.Client {
	base := u.httpClient
	if base == nil {
		base = http.DefaultClient
	}
	if len(u.spec.Headers) == 0 {
		return base
	}
	clone := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = &headerRoundTripper{base: transport, headers: u.spec.Headers}
	return &clone
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.base.RoundTrip(clone)
}

// list rebuilds the catalog from the upstream's declared capabilities. Raw
// tool schemas and metadata pass through untouched: fidelity is a correctness
// requirement, not a nicety.
func (u *upstream) list(ctx context.Context, session *mcp.ClientSession) (*catalog, error) {
	fresh := &catalog{}
	init := session.InitializeResult()
	var caps *mcp.ServerCapabilities
	if init != nil {
		fresh.instructions = init.Instructions
		caps = init.Capabilities
	}

	if caps == nil || caps.Tools != nil {
		var tools []*mcp.Tool
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list tools from %q: %w", u.spec.Name, err)
			}
			tools = append(tools, tool)
		}
		fresh.tools = filterTools(tools, u.spec.AllowedTools, u.spec.DisallowedTools)
	}

	if caps != nil && caps.Prompts != nil {
		for prompt, err := range session.Prompts(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list prompts from %q: %w", u.spec.Name, err)
			}
			if prompt == nil || prompt.Name == "" {
				continue
			}
			fresh.prompts = append(fresh.prompts, prompt)
		}
	}

	if caps != nil && caps.Resources != nil {
		for resource, err := range session.Resources(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resources from %q: %w", u.spec.Name, err)
			}
			if resource == nil || resource.URI == "" {
				continue
			}
			fresh.resources = append(fresh.resources, resource)
		}
		for template, err := range session.ResourceTemplates(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resource templates from %q: %w", u.spec.Name, err)
			}
			if template == nil || template.URITemplate == "" {
				continue
			}
			fresh.templates = append(fresh.templates, template)
		}
	}

	return fresh, nil
}

// callTool forwards one tools/call with the original tool name. A session
// that died since the last call is redialed once, transparently.
func (u *upstream) callTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	params := &mcp.CallToolParams{Name: name}
	if len(args) > 0 {
		params.Arguments = args
	}
	var result *mcp.CallToolResult
	err := u.forward(ctx, func(ctx context.Context, session *mcp.ClientSession) error {
		var callErr error
		result, callErr = session.CallTool(ctx, params)
		return callErr
	})
	return result, err
}

// getPrompt forwards one prompts/get with the original prompt name.
func (u *upstream) getPrompt(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	var result *mcp.GetPromptResult
	err := u.forward(ctx, func(ctx context.Context, session *mcp.ClientSession) error {
		var callErr error
		result, callErr = session.GetPrompt(ctx, params)
		return callErr
	})
	return result, err
}

// readResource forwards one resources/read by URI.
func (u *upstream) readResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	var result *mcp.ReadResourceResult
	err := u.forward(ctx, func(ctx context.Context, session *mcp.ClientSession) error {
		var callErr error
		result, callErr = session.ReadResource(ctx, params)
		return callErr
	})
	return result, err
}

// forward runs one upstream operation under the per-server timeout, redialing
// once when the shared session turned out to be dead.
func (u *upstream) forward(ctx context.Context, op func(context.Context, *mcp.ClientSession) error) error {
	if !u.spec.Enabled {
		return fmt.Errorf("mcp server %q is disabled", u.spec.Name)
	}
	timeout := u.spec.ToolTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u.connectMu.Lock()
	session, err := u.ensureSessionLocked(opCtx)
	u.connectMu.Unlock()
	if err != nil {
		u.markDegraded(err)
		return err
	}

	err = op(opCtx, session)
	if err == nil || !isConnectionClosed(err) {
		return err
	}

	// The shared session died underneath us; drop it and retry once on a
	// fresh dial so one upstream restart does not fail a client call.
	u.dropSession(session)
	u.connectMu.Lock()
	session, dialErr := u.ensureSessionLocked(opCtx)
	u.connectMu.Unlock()
	if dialErr != nil {
		u.markDegraded(dialErr)
		return dialErr
	}
	return op(opCtx, session)
}

func isConnectionClosed(err error) bool {
	return errors.Is(err, mcp.ErrConnectionClosed)
}

// dropSession forgets the shared session if it is still the given one.
func (u *upstream) dropSession(session *mcp.ClientSession) {
	u.stateMu.Lock()
	if u.session == session {
		u.session = nil
	}
	u.stateMu.Unlock()
	_ = session.Close()
}

func (u *upstream) markDegraded(err error) {
	u.stateMu.Lock()
	u.status = StatusDegraded
	u.lastErr = err.Error()
	u.stateMu.Unlock()
}

// close terminates the shared session.
func (u *upstream) close() {
	u.stateMu.Lock()
	session := u.session
	u.session = nil
	u.stateMu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}
