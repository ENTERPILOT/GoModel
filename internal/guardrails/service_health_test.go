package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
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
	if err := catalog.Register(func() pluginapi.Plugin { return &sidecarPlugin{state: state} }, plugins.SourceRegistered); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store := newTestStore(
		Definition{Name: "pii", Type: "sidecar", Config: json.RawMessage(`{"url":"http://presidio"}`)},
		lifecycleDefinition("plain", "one", ""),
	)
	tracker := &lifecycleTracker{}
	if err := catalog.Register(func() pluginapi.Plugin { return &lifecyclePlugin{tracker: tracker} }, plugins.SourceRegistered); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	service, err := NewService(store, catalog, plugins.HostDeps{})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	ctx := context.Background()
	if err := service.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	view, ok := service.GetView("pii")
	if !ok || view.Health != plugins.HealthOK || view.HealthError != "" || view.HealthCheckedAt == nil {
		t.Fatalf("view after first refresh = %+v, want ok with a probe time", view)
	}
	if plain, _ := service.GetView("plain"); plain.Health != plugins.HealthOK || plain.HealthCheckedAt != nil {
		t.Fatalf("plugin without a probe = %+v, want ok and never probed", plain)
	}
	if state.count() != 1 {
		t.Fatalf("probes after first refresh = %d, want 1", state.count())
	}

	state.set(errors.New("analyzer unreachable"))
	if err := service.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	view, _ = service.GetView("pii")
	if view.Health != plugins.HealthDegraded || view.HealthError != "analyzer unreachable" {
		t.Fatalf("view after failing probe = %+v, want degraded", view)
	}
	if state.count() != 2 {
		t.Fatalf("a refresh that kept the instance must re-probe it: probes = %d", state.count())
	}
	if views := service.ListViews(); len(views) != 2 || views[0].Health != plugins.HealthDegraded || views[1].Health != plugins.HealthOK {
		t.Fatalf("ListViews() = %+v", views)
	}

	// An admin change rebuilds the instance and probes the new one.
	state.set(nil)
	if err := service.Upsert(ctx, Definition{Name: "pii", Type: "sidecar", Config: json.RawMessage(`{"url":"http://presidio:5002"}`)}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	view, _ = service.GetView("pii")
	if view.Health != plugins.HealthOK || view.HealthError != "" {
		t.Fatalf("view after upsert = %+v, want ok", view)
	}
}
