package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/enterpilot/gomodel/internal/version"
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
	// closed marks a permanently disposed upstream (removed or replaced by a
	// reconcile). A stale background refresh must not redial it and leak an
	// untracked session.
	closed bool
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
	if !u.spec.Enabled || u.isClosed() {
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
		// A session whose listing failed is suspect (dead transport, wedged
		// stream). Drop it so the next re-probe starts from a fresh dial
		// instead of reusing it forever; refreshes are infrequent, so the
		// redial cost is negligible next to guaranteed recovery.
		u.dropSession(session)
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
	closed := u.closed
	u.stateMu.Unlock()
	if closed {
		return nil, fmt.Errorf("mcp server %q was removed", u.spec.Name)
	}
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
	// close() may have disposed the upstream while the dial was in flight
	// (close deliberately does not wait on connectMu). Storing the session
	// then would leak it forever, so discard it instead.
	if u.closed {
		u.stateMu.Unlock()
		_ = session.Close()
		return nil, fmt.Errorf("mcp server %q was removed", u.spec.Name)
	}
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
		// Start from a minimal environment, not os.Environ(): the gateway
		// process holds every provider API key and the master key, and a
		// compromised MCP server binary must not inherit them. Operators pass
		// anything else explicitly via the server's env map (${VAR} expands
		// in config), on top of the basics process launchers need. The
		// non-nil initialization matters: a nil cmd.Env would fall back to
		// inheriting the full parent environment.
		cmd.Env = []string{}
		for _, key := range []string{"PATH", "HOME", "TMPDIR", "USER", "LANG"} {
			if value := os.Getenv(key); value != "" {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
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
	clone.Transport = &headerRoundTripper{
		base:    transport,
		headers: u.spec.Headers,
		origin:  requestOrigin(u.spec.URL),
	}
	return &clone
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	origin  string
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.origin != "" && requestOrigin(req.URL.String()) == t.origin {
		for key, value := range t.headers {
			clone.Header.Set(key, value)
		}
	}
	return t.base.RoundTrip(clone)
}

func requestOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

// list rebuilds the catalog from the upstream's declared capabilities. Valid
// tool schemas and metadata pass through untouched; malformed schemas are
// made safe for the stricter downstream SDK.
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

	// Deterministic ordering across the whole catalog, not just tools: clients
	// that embed capability lists in prompts get stable prompt-cache keys.
	slices.SortFunc(fresh.prompts, func(a, b *mcp.Prompt) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(fresh.resources, func(a, b *mcp.Resource) int {
		return strings.Compare(a.URI, b.URI)
	})
	slices.SortFunc(fresh.templates, func(a, b *mcp.ResourceTemplate) int {
		return strings.Compare(a.URITemplate, b.URITemplate)
	})

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

func (u *upstream) isClosed() bool {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.closed
}

// reset drops the shared session so the next use redials. The upstream stays
// alive — this is the "force reconnect" path, not disposal.
func (u *upstream) reset() {
	u.stateMu.Lock()
	session := u.session
	u.session = nil
	u.stateMu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

// close permanently disposes the upstream: the session is terminated and any
// in-flight background refresh is refused a redial.
func (u *upstream) close() {
	u.stateMu.Lock()
	u.closed = true
	session := u.session
	u.session = nil
	u.stateMu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}
