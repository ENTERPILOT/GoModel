package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
)

// sidecarPlugin reports the health of a fake external dependency.
type sidecarPlugin struct {
	state *sidecarState
}

type sidecarState struct {
	mu     sync.Mutex
	err    error
	probes int
}

func (s *sidecarState) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *sidecarState) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes
}

func (p *sidecarPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "sidecar", Kinds: []pluginapi.Kind{pluginapi.KindPrompt}, Guardrail: true, ConfigSchema: []pluginapi.Field{{Key: "url", Input: pluginapi.InputText}}}
}
func (p *sidecarPlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (p *sidecarPlugin) Close(context.Context) error                                 { return nil }
func (p *sidecarPlugin) OnPrompt(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}
func (p *sidecarPlugin) Health(context.Context) error {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	p.state.probes++
	return p.state.err
}

func TestServiceProbesInstanceHealthOnRefresh(t *testing.T) {
	state := &sidecarState{}
	catalog := plugins.NewCatalog()
	err := catalog.Register(func() pluginapi.Plugin { return &sidecarPlugin{state: state} }, plugins.SourceRegistered)
	require.NoError(t, err)

	store := newTestStore(
		Definition{Name: "pii", Type: "sidecar", Config: json.RawMessage(`{"url":"http://presidio"}`)},
		lifecycleDefinition("plain", "one", ""),
	)
	tracker := &lifecycleTracker{}
	err = catalog.Register(func() pluginapi.Plugin { return &lifecyclePlugin{tracker: tracker} }, plugins.SourceRegistered)
	require.NoError(t, err)

	service, err := NewService(store, catalog, plugins.HostDeps{})
	require.NoError(t, err)

	ctx := context.Background()
	err = service.Refresh(ctx)
	require.NoError(t, err)

	view, ok := service.GetView("pii")
	require.True(t, ok)
	require.Equal(t, plugins.HealthOK, view.Health)
	require.Empty(t, view.HealthError)
	require.NotNil(t, view.HealthCheckedAt)
	plain, _ := service.GetView("plain")
	require.Equal(t, plugins.HealthOK, plain.Health)
	require.Nil(t, plain.HealthCheckedAt, "plugin without a probe = %+v, want ok and never probed", plain)
	require.Equal(t, 1, state.count())

	state.set(errors.New("analyzer unreachable"))
	err = service.Refresh(ctx)
	require.NoError(t, err)

	view, _ = service.GetView("pii")
	require.Equal(t, plugins.HealthDegraded, view.Health)
	require.Equal(t, "analyzer unreachable", view.HealthError, "view after failing probe = %+v, want degraded", view)
	require.Equal(t, 2, state.count())
	views := service.ListViews()
	require.Len(t, views, 2)
	require.Equal(t, plugins.HealthDegraded, views[0].Health)
	require.Equal(t, plugins.HealthOK, views[1].Health)

	// An admin change rebuilds the instance and probes the new one.
	state.set(nil)
	err = service.Upsert(ctx, Definition{Name: "pii", Type: "sidecar", Config: json.RawMessage(`{"url":"http://presidio:5002"}`)})
	require.NoError(t, err)

	view, _ = service.GetView("pii")
	require.Equal(t, plugins.HealthOK, view.Health)
	require.Empty(t, view.HealthError, "view after upsert = %+v, want ok", view)
}

func TestServiceProbeHealthIgnoresCallerCancellation(t *testing.T) {
	state := &sidecarState{}
	catalog := plugins.NewCatalog()
	err := catalog.Register(func() pluginapi.Plugin { return &sidecarPlugin{state: state} }, plugins.SourceRegistered)
	require.NoError(t, err)

	service, err := NewService(newTestStore(Definition{Name: "pii", Type: "sidecar", Config: json.RawMessage(`{"url":"http://presidio"}`)}), catalog, plugins.HostDeps{})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.mu.RLock()
	snap := service.snapshot
	service.mu.RUnlock()
	service.probeHealth(ctx, snap)
	view, _ := service.GetView("pii")
	require.Equal(t, plugins.HealthOK, view.Health)
	require.Empty(t, view.HealthError, "view after a probe under a cancelled caller context = %+v, want ok", view)
	require.Equal(t, 2, state.count())
}
