package mcpgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reprobeInterval is how often degraded or never-connected upstreams are
// retried, mirroring PROVIDER_RECHECK_INTERVAL for model providers.
const reprobeInterval = 60 * time.Second

// refreshInterval is how often healthy upstream catalogs are re-listed as a
// safety net for servers that never emit list_changed notifications.
const refreshInterval = 5 * time.Minute

// Manager owns the set of upstream connections and their catalogs.
type Manager struct {
	httpClient *http.Client

	mu        sync.RWMutex
	upstreams map[string]*upstream

	stopOnce sync.Once
	stop     chan struct{}
}

// NewManager creates an empty manager. Apply installs the initial specs.
func NewManager(httpClient *http.Client) *Manager {
	m := &Manager{
		httpClient: httpClient,
		upstreams:  make(map[string]*upstream),
		stop:       make(chan struct{}),
	}
	go m.maintain()
	return m
}

// Apply reconciles the running upstreams with the desired specs: removed
// servers are closed, new servers are added, changed servers are redialed.
// Unchanged servers keep their live session and catalog. Initial connects run
// asynchronously so startup and admin edits never block on upstream IO.
func (m *Manager) Apply(specs []ServerSpec) {
	desired := make(map[string]ServerSpec, len(specs))
	for _, spec := range specs {
		desired[spec.Name] = spec
	}

	var toRefresh []*upstream
	var toClose []*upstream

	m.mu.Lock()
	for name, existing := range m.upstreams {
		spec, keep := desired[name]
		if keep && existing.spec.equal(spec) {
			delete(desired, name)
			continue
		}
		toClose = append(toClose, existing)
		delete(m.upstreams, name)
		_ = spec
	}
	for name, spec := range desired {
		fresh := newUpstream(spec, m.httpClient)
		m.upstreams[name] = fresh
		if spec.Enabled {
			toRefresh = append(toRefresh, fresh)
		}
	}
	m.mu.Unlock()

	for _, u := range toClose {
		u.close()
	}
	for _, u := range toRefresh {
		go func(u *upstream) {
			if err := u.refresh(context.Background()); err != nil {
				slog.Warn("mcp server initial connect failed; will re-probe",
					"server", u.spec.Name, "error", err)
			} else {
				view := u.view()
				slog.Info("mcp server connected",
					"server", u.spec.Name, "tools", view.ToolCount)
			}
		}(u)
	}
}

// maintain re-probes degraded upstreams and periodically re-lists healthy
// catalogs until Close.
func (m *Manager) maintain() {
	reprobe := time.NewTicker(reprobeInterval)
	relist := time.NewTicker(refreshInterval)
	defer reprobe.Stop()
	defer relist.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-reprobe.C:
			m.refreshWhere(func(status ServerStatus) bool {
				return status == StatusDegraded || status == StatusConnecting
			})
		case <-relist.C:
			m.refreshWhere(func(status ServerStatus) bool {
				return status == StatusConnected
			})
		}
	}
}

func (m *Manager) refreshWhere(match func(ServerStatus) bool) {
	for _, u := range m.list() {
		if !u.spec.Enabled {
			continue
		}
		if _, status := u.snapshot(); !match(status) {
			continue
		}
		go func(u *upstream) {
			if err := u.refresh(context.Background()); err != nil {
				slog.Debug("mcp server refresh failed", "server", u.spec.Name, "error", err)
			}
		}(u)
	}
}

func (m *Manager) list() []*upstream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*upstream, 0, len(m.upstreams))
	for _, u := range m.upstreams {
		result = append(result, u)
	}
	return result
}

func (m *Manager) get(name string) (*upstream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.upstreams[name]
	return u, ok
}

// Views returns admin snapshots of every upstream, sorted by name.
func (m *Manager) Views() []ServerView {
	upstreams := m.list()
	views := make([]ServerView, 0, len(upstreams))
	for _, u := range upstreams {
		views = append(views, u.view())
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Spec.Name < views[j].Spec.Name })
	return views
}

// Reconnect force-redials one server and relists its catalog synchronously.
func (m *Manager) Reconnect(ctx context.Context, name string) (ServerView, error) {
	u, ok := m.get(name)
	if !ok {
		return ServerView{}, fmt.Errorf("mcp server %q is not configured", name)
	}
	u.reset()
	if u.spec.Enabled {
		if err := u.refresh(ctx); err != nil {
			return u.view(), err
		}
	}
	return u.view(), nil
}

// CallTool forwards one tool call to the named server using the original
// (un-prefixed) tool name.
func (m *Manager) CallTool(ctx context.Context, server, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	u, ok := m.get(server)
	if !ok {
		return nil, fmt.Errorf("mcp server %q is not configured", server)
	}
	return u.callTool(ctx, tool, args)
}

// GetPrompt forwards one prompts/get to the named server.
func (m *Manager) GetPrompt(ctx context.Context, server string, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	u, ok := m.get(server)
	if !ok {
		return nil, fmt.Errorf("mcp server %q is not configured", server)
	}
	return u.getPrompt(ctx, params)
}

// ReadResource forwards one resources/read to the named server.
func (m *Manager) ReadResource(ctx context.Context, server string, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	u, ok := m.get(server)
	if !ok {
		return nil, fmt.Errorf("mcp server %q is not configured", server)
	}
	return u.readResource(ctx, params)
}

// Close terminates the maintenance loop and every upstream session.
func (m *Manager) Close() {
	m.stopOnce.Do(func() { close(m.stop) })
	for _, u := range m.list() {
		u.close()
	}
}
