package mcpgateway

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/version"
)

// sessionIdleTimeout closes downstream sessions that stop sending requests,
// and bounds the session-binding registry.
const sessionIdleTimeout = 30 * time.Minute

// ScopeHeader restricts a request's visible servers to a comma-separated
// subset, so one gateway key can serve differently-scoped clients without
// extra endpoints. Unknown names are ignored; an empty header means all.
const ScopeHeader = "X-MCP-Servers"

// labelsHeader carries the request labels resolved by the gateway (tagging
// headers plus auth-key labels) from the HTTP layer into tool handlers, which
// only see per-message HTTP headers. It is internal: the HTTP entrypoint
// strips any client-sent value before stamping its own.
const labelsHeader = "X-Gomodel-Mcp-Labels"

// pinnedServerKey carries the /mcp/{server} path pin through the request
// context into per-session server construction.
type pinnedServerKey struct{}

// Service is the MCP gateway: it merges declarative and admin-store server
// specs into the upstream manager and serves the downstream MCP endpoints.
type Service struct {
	manager        *Manager
	store          Store
	usageLogger    usage.LoggerInterface
	userPathHeader string
	configSpecs    map[string]ServerSpec

	handler http.Handler

	bindMu   sync.Mutex
	bindings map[string]sessionBinding

	requestMu      sync.Mutex
	requestCancels map[uint64]context.CancelFunc
	nextRequestID  uint64
	closing        bool

	stopOnce sync.Once
	stop     chan struct{}
}

// sessionBinding pins a downstream MCP session to the user path it was
// initialized under. Bearer auth still runs on every request; the binding
// additionally stops a *different* principal from riding a leaked session ID,
// per the MCP session-hijacking guidance.
type sessionBinding struct {
	authKeyID string
	userPath  string
	pinned    string
	lastSeen  time.Time
}

// Options configures NewService.
type Options struct {
	// ConfigServers are the declarative servers from config.yaml / MCP_SERVERS.
	ConfigServers map[string]ServerSpec
	// Store persists admin-managed servers. Optional.
	Store Store
	// HTTPClient is the shared outbound HTTP client for http/sse upstreams.
	HTTPClient *http.Client
	// UsageLogger records one usage entry per tool call. Optional.
	UsageLogger usage.LoggerInterface
	// UserPathHeader is the configured user-path header name.
	UserPathHeader string
}

// NewService builds the gateway service and starts connecting to the merged
// server set. Upstream connects are asynchronous; construction never blocks.
func NewService(ctx context.Context, opts Options) (*Service, error) {
	s := &Service{
		manager:        NewManager(opts.HTTPClient),
		store:          opts.Store,
		usageLogger:    opts.UsageLogger,
		userPathHeader: core.UserPathHeaderName(opts.UserPathHeader),
		configSpecs:    opts.ConfigServers,
		bindings:       make(map[string]sessionBinding),
		requestCancels: make(map[uint64]context.CancelFunc),
		stop:           make(chan struct{}),
	}
	streamable := mcp.NewStreamableHTTPHandler(s.getServer, &mcp.StreamableHTTPOptions{
		SessionTimeout: sessionIdleTimeout,
		Logger:         slog.Default(),
	})
	s.handler = http.NewCrossOriginProtection().Handler(streamable)
	if err := s.Reload(ctx); err != nil {
		s.Close()
		return nil, err
	}
	go s.sweepBindings()
	return s, nil
}

// Reload re-merges declarative and store specs and reconciles the upstream
// set. Declarative entries shadow store rows with the same name, mirroring
// the tagging/virtual-models source precedence.
func (s *Service) Reload(ctx context.Context) error {
	specs := make([]ServerSpec, 0, len(s.configSpecs))
	seen := make(map[string]struct{}, len(s.configSpecs))
	for name, spec := range s.configSpecs {
		specs = append(specs, spec)
		seen[name] = struct{}{}
	}
	if s.store != nil {
		rows, err := s.store.List(ctx)
		if err != nil {
			return fmt.Errorf("list managed mcp servers: %w", err)
		}
		for _, row := range rows {
			if _, shadowed := seen[row.Name]; shadowed {
				slog.Warn("mcp server from admin store is shadowed by config", "server", row.Name)
				continue
			}
			specs = append(specs, row.Spec())
		}
	}
	s.manager.Apply(specs)
	return nil
}

// Views returns the current admin snapshot of all servers.
func (s *Service) Views() []ServerView {
	return s.manager.Views()
}

// IsManaged reports whether name is declared in config/env (read-only).
func (s *Service) IsManaged(name string) bool {
	_, ok := s.configSpecs[name]
	return ok
}

// Upsert validates and persists one admin-managed server, then reconciles.
// Config-declared names are read-only; stdio definitions are rejected at the
// store boundary (declarative-only by design — see the gateway spec).
func (s *Service) Upsert(ctx context.Context, server ManagedServer) error {
	if s.store == nil {
		return fmt.Errorf("mcp server persistence is unavailable")
	}
	if s.IsManaged(server.Name) {
		return fmt.Errorf("mcp server %q is managed by config/env and is read-only", server.Name)
	}
	if err := server.Validate(); err != nil {
		return err
	}
	if err := s.store.Upsert(ctx, server); err != nil {
		return err
	}
	if err := s.Reload(ctx); err != nil {
		// The row is persisted; only applying it to the running manager
		// failed. Say so — a retry or restart picks the row up.
		return fmt.Errorf("mcp server %q was saved but not applied: %w", server.Name, err)
	}
	return nil
}

// GetManaged returns one admin-managed server row from the store. Config-
// declared servers are not store rows, so they (and a missing store) report
// ErrNotFound.
func (s *Service) GetManaged(ctx context.Context, name string) (*ManagedServer, error) {
	if s.store == nil {
		return nil, ErrNotFound
	}
	return s.store.Get(ctx, name)
}

// Delete removes one admin-managed server, then reconciles.
func (s *Service) Delete(ctx context.Context, name string) error {
	if s.store == nil {
		return fmt.Errorf("mcp server persistence is unavailable")
	}
	if s.IsManaged(name) {
		return fmt.Errorf("mcp server %q is managed by config/env and is read-only", name)
	}
	if err := s.store.Delete(ctx, name); err != nil {
		return err
	}
	if err := s.Reload(ctx); err != nil {
		return fmt.Errorf("mcp server %q was deleted but the running set was not updated: %w", name, err)
	}
	return nil
}

// Reconnect force-redials one server and returns its fresh state.
func (s *Service) Reconnect(ctx context.Context, name string) (ServerView, error) {
	return s.manager.Reconnect(ctx, name)
}

// Close stops background work, terminates upstream sessions, and cancels
// downstream HTTP exchanges. Streamable HTTP clients keep a GET request open
// for server events, so those request contexts must be ended before the HTTP
// server can complete its graceful drain.
func (s *Service) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)

		s.requestMu.Lock()
		s.closing = true
		cancels := make([]context.CancelFunc, 0, len(s.requestCancels))
		for _, cancel := range s.requestCancels {
			cancels = append(cancels, cancel)
		}
		s.requestMu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}

		s.manager.Close()
	})
}

// ServeHTTP handles one downstream MCP HTTP exchange. pinnedServer is the
// /mcp/{server} path segment ("" for the aggregated endpoint). Gateway
// authentication has already run; this layer enforces session-to-principal
// binding and stamps the internal identity headers tool handlers read.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request, pinnedServer string) error {
	requestCtx, cancel := context.WithCancel(r.Context())
	s.requestMu.Lock()
	if s.closing {
		s.requestMu.Unlock()
		cancel()
		return fmt.Errorf("MCP gateway is shutting down")
	}
	requestID := s.nextRequestID
	s.nextRequestID++
	s.requestCancels[requestID] = cancel
	s.requestMu.Unlock()
	defer func() {
		cancel()
		s.requestMu.Lock()
		delete(s.requestCancels, requestID)
		s.requestMu.Unlock()
	}()
	r = r.WithContext(requestCtx)

	userPath := core.UserPathFromContext(r.Context())
	authKeyID := core.GetAuthKeyID(r.Context())

	if pinnedServer != "" {
		view, ok := s.findVisibleServer(pinnedServer, userPath)
		if !ok {
			return core.NewNotFoundError("unknown mcp server: " + pinnedServer)
		}
		if !view.Spec.Enabled {
			return core.NewNotFoundError("mcp server is disabled: " + pinnedServer)
		}
	}

	if sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id")); sessionID != "" {
		if !s.touchBinding(sessionID, authKeyID, userPath, pinnedServer, r.Method == http.MethodDelete) {
			// A different principal presented this session ID. Report the
			// session as gone (404 per the transport spec) so the legitimate
			// client's session stays unaffected and this caller re-initializes.
			return core.NewNotFoundError("unknown MCP session")
		}
	}

	// The labels header is internal: never trust an inbound value.
	r.Header.Del(labelsHeader)
	if labels := core.RequestLabelsFromContext(r.Context()); len(labels) > 0 {
		r.Header.Set(labelsHeader, strings.Join(labels, ","))
	}

	r = r.WithContext(context.WithValue(r.Context(), pinnedServerKey{}, pinnedServer))
	s.handler.ServeHTTP(w, r)
	return nil
}

// requestScope captures the visibility inputs of one downstream session.
type requestScope struct {
	authKeyID string
	userPath  string
	pinned    string
	include   map[string]struct{}
}

func (s *Service) scopeFromRequest(r *http.Request) requestScope {
	scope := requestScope{
		authKeyID: core.GetAuthKeyID(r.Context()),
		userPath:  core.UserPathFromContext(r.Context()),
	}
	if pinned, ok := r.Context().Value(pinnedServerKey{}).(string); ok {
		scope.pinned = pinned
	}
	if raw := r.Header.Get(ScopeHeader); raw != "" {
		scope.include = make(map[string]struct{})
		for name := range strings.SplitSeq(raw, ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				scope.include[trimmed] = struct{}{}
			}
		}
	}
	return scope
}

// getServer builds the per-session MCP server for one downstream client: the
// SDK calls it when a new session initializes. The tool/prompt/resource view
// is filtered to what this principal may see — clients never discover tools
// they cannot call — and stays a stable snapshot for the session's lifetime.
func (s *Service) getServer(r *http.Request) *mcp.Server {
	scope := s.scopeFromRequest(r)
	views := s.visibleServers(scope)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "gomodel",
		Title:   "GoModel MCP Gateway",
		Version: version.Version,
	}, &mcp.ServerOptions{
		Instructions: s.composeInstructions(scope, views),
		// Advertise the full feature set even when the visible catalog is
		// currently empty, so list calls return empty lists instead of
		// method-not-supported errors.
		Capabilities: &mcp.ServerCapabilities{
			Tools:     &mcp.ToolCapabilities{},
			Prompts:   &mcp.PromptCapabilities{},
			Resources: &mcp.ResourceCapabilities{},
		},
		GetSessionID: func() string {
			id := rand.Text()
			s.bindSession(id, scope.authKeyID, scope.userPath, scope.pinned)
			return id
		},
	})

	endpoint := "/mcp"
	prefixNames := scope.pinned == ""
	if !prefixNames {
		endpoint = "/mcp/" + scope.pinned
	}

	toolOwners := make(map[string]string)
	promptOwners := make(map[string]string)
	resourceOwners := make(map[string]string)
	for _, view := range views {
		snapshot, _ := s.upstreamCatalog(view.Spec.Name)
		if snapshot == nil {
			continue
		}
		s.registerTools(server, view.Spec.Name, snapshot, prefixNames, endpoint, toolOwners)
		s.registerPrompts(server, view.Spec.Name, snapshot, prefixNames, promptOwners)
		s.registerResources(server, view.Spec.Name, snapshot, resourceOwners)
	}
	return server
}

func (s *Service) upstreamCatalog(name string) (*catalog, ServerStatus) {
	u, ok := s.manager.get(name)
	if !ok {
		return nil, StatusDisabled
	}
	return u.snapshot()
}

// visibleServers filters the upstream set down to one request's view:
// enabled, allowed for the user path, and inside the pin/header scope.
func (s *Service) visibleServers(scope requestScope) []ServerView {
	views := s.manager.Views()
	visible := make([]ServerView, 0, len(views))
	for _, view := range views {
		if !view.Spec.Enabled {
			continue
		}
		if scope.pinned != "" && view.Spec.Name != scope.pinned {
			continue
		}
		if scope.include != nil {
			if _, ok := scope.include[view.Spec.Name]; !ok {
				continue
			}
		}
		if !userPathAllowed(scope.userPath, view.Spec.UserPaths) {
			continue
		}
		visible = append(visible, view)
	}
	return visible
}

func (s *Service) findVisibleServer(name, userPath string) (ServerView, bool) {
	for _, view := range s.manager.Views() {
		if view.Spec.Name != name {
			continue
		}
		if !userPathAllowed(userPath, view.Spec.UserPaths) {
			return ServerView{}, false
		}
		return view, true
	}
	return ServerView{}, false
}

// composeInstructions merges upstream instructions into the gateway's own,
// so guidance written for an upstream server survives aggregation.
func (s *Service) composeInstructions(scope requestScope, views []ServerView) string {
	var b strings.Builder
	if scope.pinned == "" {
		names := make([]string, 0, len(views))
		for _, view := range views {
			names = append(names, view.Spec.Name)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "GoModel MCP gateway aggregating %d server(s): %s. Tools and prompts are namespaced as {server}%s{name}.",
			len(names), strings.Join(names, ", "), namespaceSeparator)
	}
	for _, view := range views {
		snapshot, _ := s.upstreamCatalog(view.Spec.Name)
		if snapshot == nil || strings.TrimSpace(snapshot.instructions) == "" {
			continue
		}
		if b.Len() > 0 {
			displayName := strings.TrimSpace(view.Spec.DisplayName)
			if displayName == "" || displayName == view.Spec.Name {
				fmt.Fprintf(&b, "\n\n## %s\n", view.Spec.Name)
			} else {
				fmt.Fprintf(&b, "\n\n## %s (`%s`)\n", displayName, view.Spec.Name)
			}
		}
		b.WriteString(strings.TrimSpace(snapshot.instructions))
	}
	return b.String()
}

// registerTools adds one upstream's tools to a session server. Tool metadata
// and valid schemas relay verbatim; only the name is prefixed on the
// aggregated endpoint. Arguments relay as raw JSON — validation belongs to
// the upstream.
func (s *Service) registerTools(server *mcp.Server, upstreamName string, snapshot *catalog, prefix bool, endpoint string, owners map[string]string) {
	for _, tool := range snapshot.tools {
		exposed := tool.Name
		if prefix {
			exposed = NamespacedName(upstreamName, tool.Name)
		}
		if owner, taken := owners[exposed]; taken {
			slog.Warn("mcp tool name collision; keeping first server",
				"tool", exposed, "kept", owner, "skipped", upstreamName)
			continue
		}
		owners[exposed] = upstreamName
		clone := *tool
		clone.Name = exposed
		server.AddTool(&clone, s.toolHandler(upstreamName, tool.Name, exposed, endpoint))
	}
}

func (s *Service) toolHandler(upstreamName, toolName, exposedName, endpoint string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now()
		result, err := s.manager.CallTool(ctx, upstreamName, toolName, req.Params.Arguments)
		s.recordToolCall(req, upstreamName, exposedName, endpoint, started, result, err)
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", upstreamName, err)
		}
		return result, nil
	}
}

// registerPrompts mirrors registerTools for prompts.
func (s *Service) registerPrompts(server *mcp.Server, upstreamName string, snapshot *catalog, prefix bool, owners map[string]string) {
	for _, prompt := range snapshot.prompts {
		exposed := prompt.Name
		if prefix {
			exposed = NamespacedName(upstreamName, prompt.Name)
		}
		if owner, taken := owners[exposed]; taken {
			slog.Warn("mcp prompt name collision; keeping first server",
				"prompt", exposed, "kept", owner, "skipped", upstreamName)
			continue
		}
		owners[exposed] = upstreamName
		clone := *prompt
		clone.Name = exposed
		originalName := prompt.Name
		server.AddPrompt(&clone, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			params := *req.Params
			params.Name = originalName
			result, err := s.manager.GetPrompt(ctx, upstreamName, &params)
			if err != nil {
				return nil, fmt.Errorf("mcp server %q: %w", upstreamName, err)
			}
			return result, nil
		})
	}
}

// registerResources adds resources and resource templates. Resource URIs are
// globally meaningful, so a URI already claimed by an earlier server is
// skipped with a warning rather than silently re-routed.
func (s *Service) registerResources(server *mcp.Server, upstreamName string, snapshot *catalog, owners map[string]string) {
	read := func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		result, err := s.manager.ReadResource(ctx, upstreamName, req.Params)
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", upstreamName, err)
		}
		return result, nil
	}
	for _, resource := range snapshot.resources {
		if owner, taken := owners[resource.URI]; taken {
			slog.Warn("mcp resource URI collision; keeping first server",
				"uri", resource.URI, "kept", owner, "skipped", upstreamName)
			continue
		}
		owners[resource.URI] = upstreamName
		server.AddResource(resource, read)
	}
	for _, template := range snapshot.templates {
		key := "template:" + template.URITemplate
		if owner, taken := owners[key]; taken {
			slog.Warn("mcp resource template collision; keeping first server",
				"template", template.URITemplate, "kept", owner, "skipped", upstreamName)
			continue
		}
		owners[key] = upstreamName
		server.AddResourceTemplate(template, read)
	}
}

// bindSession records the principal a new session was initialized under.
func (s *Service) bindSession(sessionID, authKeyID, userPath, pinned string) {
	s.bindMu.Lock()
	s.bindings[sessionID] = sessionBinding{
		authKeyID: authKeyID,
		userPath:  userPath,
		pinned:    pinned,
		lastSeen:  time.Now(),
	}
	s.bindMu.Unlock()
}

// touchBinding refreshes a known session binding and reports whether the
// caller's authenticated identity, user path, and endpoint pin match it.
// Unknown session IDs pass through: the SDK rejects them itself, and bindings
// do not survive restarts.
func (s *Service) touchBinding(sessionID, authKeyID, userPath, pinned string, remove bool) bool {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	binding, ok := s.bindings[sessionID]
	if !ok {
		return true
	}
	if binding.authKeyID != authKeyID || binding.userPath != userPath || binding.pinned != pinned {
		return false
	}
	if remove {
		delete(s.bindings, sessionID)
		return true
	}
	binding.lastSeen = time.Now()
	s.bindings[sessionID] = binding
	return true
}

// sweepBindings evicts bindings whose sessions idled out, tracking the SDK's
// own session timeout.
func (s *Service) sweepBindings() {
	ticker := time.NewTicker(sessionIdleTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-sessionIdleTimeout - time.Minute)
			s.bindMu.Lock()
			for id, binding := range s.bindings {
				if binding.lastSeen.Before(cutoff) {
					delete(s.bindings, id)
				}
			}
			s.bindMu.Unlock()
		}
	}
}
