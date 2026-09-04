package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// lifecyclePlugin counts how many instances were built and closed.
type lifecyclePlugin struct {
	tracker *lifecycleTracker
	closed  bool
}

type lifecycleTracker struct {
	mu     sync.Mutex
	built  int
	closed int
}

func (t *lifecycleTracker) counts() (built, closed int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.built, t.closed
}

func (p *lifecyclePlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:         "lifecycle",
		Kinds:        []pluginapi.Kind{pluginapi.KindPrompt},
		ConfigSchema: []pluginapi.Field{{Key: "word", Input: pluginapi.InputText}},
	}
}

func (p *lifecyclePlugin) Init(context.Context, json.RawMessage, pluginapi.Host) error {
	p.tracker.mu.Lock()
	p.tracker.built++
	p.tracker.mu.Unlock()
	return nil
}

func (p *lifecyclePlugin) Close(context.Context) error {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	if p.closed {
		return errors.New("closed twice")
	}
	p.closed = true
	p.tracker.closed++
	return nil
}

func (p *lifecyclePlugin) OnPrompt(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

func lifecycleService(t *testing.T, store *testStore) (*Service, *lifecycleTracker, *time.Time) {
	t.Helper()
	tracker := &lifecycleTracker{}
	catalog := plugins.NewCatalog()
	if err := catalog.Register(func() pluginapi.Plugin { return &lifecyclePlugin{tracker: tracker} }, plugins.SourceRegistered); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	service, err := NewService(store, catalog, plugins.HostDeps{})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	service.retireAfter = time.Minute
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service, tracker, &clock
}

func lifecycleDefinition(name, word, description string) Definition {
	return Definition{Name: name, Type: "lifecycle", Description: description, Config: json.RawMessage(`{"word":"` + word + `"}`)}
}

func TestServiceRefreshKeepsUnchangedInstances(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""), lifecycleDefinition("b", "two", ""))
	service, tracker, _ := lifecycleService(t, store)
	before := service.snapshot.instances["a"]

	store.definitions["a"] = lifecycleDefinition("a", "one", "described differently")
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if service.snapshot.instances["a"] != before {
		t.Fatal("unchanged definition rebuilt its instance")
	}
	if built, closed := tracker.counts(); built != 2 || closed != 0 {
		t.Fatalf("built = %d, closed = %d, want 2 built and none closed", built, closed)
	}
}

func TestServiceRetiresReplacedInstancesAfterGrace(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""))
	service, tracker, clock := lifecycleService(t, store)
	before := service.snapshot.instances["a"]

	if err := service.Upsert(context.Background(), lifecycleDefinition("a", "changed", "")); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if service.snapshot.instances["a"] == before {
		t.Fatal("changed config kept the old instance")
	}
	if built, closed := tracker.counts(); built != 2 || closed != 0 {
		t.Fatalf("after replace: built = %d, closed = %d, want the old instance still open", built, closed)
	}

	*clock = clock.Add(30 * time.Second)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if _, closed := tracker.counts(); closed != 0 {
		t.Fatal("retired instance closed before its grace period")
	}

	*clock = clock.Add(31 * time.Second)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if built, closed := tracker.counts(); built != 2 || closed != 1 {
		t.Fatalf("after grace: built = %d, closed = %d, want the replaced instance closed", built, closed)
	}

	if err := service.Delete(context.Background(), "a"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	*clock = clock.Add(2 * time.Minute)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if _, closed := tracker.counts(); closed != 2 {
		t.Fatalf("closed = %d, want the deleted instance closed too", closed)
	}
}

func TestServiceClosesFreshInstancesWhenPersistFails(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""))
	service, tracker, _ := lifecycleService(t, store)
	before := service.snapshot.instances["a"]

	store.upsertErr = errors.New("db down")
	if err := service.Upsert(context.Background(), lifecycleDefinition("a", "changed", "")); err == nil {
		t.Fatal("Upsert() error = nil, want persistence failure")
	}
	if service.snapshot.instances["a"] != before {
		t.Fatal("failed upsert swapped the snapshot")
	}
	if built, closed := tracker.counts(); built != 2 || closed != 1 {
		t.Fatalf("built = %d, closed = %d, want the never-served instance closed", built, closed)
	}
}

func TestServiceCloseClosesActiveAndRetiredInstances(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""), lifecycleDefinition("b", "two", ""))
	service, tracker, _ := lifecycleService(t, store)
	if err := service.Upsert(context.Background(), lifecycleDefinition("a", "changed", "")); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if built, closed := tracker.counts(); built != 3 || closed != 3 {
		t.Fatalf("built = %d, closed = %d, want everything closed once", built, closed)
	}
	if service.Len() != 0 {
		t.Fatal("snapshot not emptied by Close")
	}
}
