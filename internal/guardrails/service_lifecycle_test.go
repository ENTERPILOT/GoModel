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
	"github.com/stretchr/testify/require"
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
		Guardrail:    true,
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
	err := catalog.Register(func() pluginapi.Plugin { return &lifecyclePlugin{tracker: tracker} }, plugins.SourceRegistered)
	require.NoError(t, err)

	service, err := NewService(store, catalog, plugins.HostDeps{})
	require.NoError(t, err)

	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	service.retireAfter = time.Minute
	err = service.Refresh(context.Background())
	require.NoError(t, err)

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
	err := service.Refresh(context.Background())
	require.NoError(t, err)
	require.Same(t, before, service.snapshot.instances["a"])
	built, closed := tracker.counts()
	require.Equal(t, 2, built)
	require.Equal(t, 0, closed)
}

func TestServiceRetiresReplacedInstancesAfterGrace(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""))
	service, tracker, clock := lifecycleService(t, store)
	before := service.snapshot.instances["a"]
	err := service.Upsert(context.Background(), lifecycleDefinition("a", "changed", ""))
	require.NoError(t, err)
	require.NotSame(t, before, service.snapshot.instances["a"])
	built, closed := tracker.counts()
	require.Equal(t, 2, built)
	require.Equal(t, 0, closed)

	*clock = clock.Add(30 * time.Second)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	_, closed = tracker.counts()
	require.Equal(t, 0, closed)

	*clock = clock.Add(31 * time.Second)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	built, closed = tracker.counts()
	require.Equal(t, 2, built)
	require.Equal(t, 1, closed)
	err = service.Delete(context.Background(), "a")
	require.NoError(t, err)

	*clock = clock.Add(2 * time.Minute)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	_, closed = tracker.counts()
	require.Equal(t, 2, closed)
}

func TestServiceClosesFreshInstancesWhenPersistFails(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""))
	service, tracker, _ := lifecycleService(t, store)
	before := service.snapshot.instances["a"]

	store.upsertErr = errors.New("db down")
	require.Error(t, service.Upsert(context.Background(), lifecycleDefinition("a", "changed", "")))
	require.Same(t, before, service.snapshot.instances["a"])
	built, closed := tracker.counts()
	require.Equal(t, 2, built)
	require.Equal(t, 1, closed)
}

func TestServiceCloseClosesActiveAndRetiredInstances(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""), lifecycleDefinition("b", "two", ""))
	service, tracker, _ := lifecycleService(t, store)
	err := service.Upsert(context.Background(), lifecycleDefinition("a", "changed", ""))
	require.NoError(t, err)
	err = service.Close(context.Background())
	require.NoError(t, err)
	built, closed := tracker.counts()
	require.Equal(t, 3, built)
	require.Equal(t, 3, closed)
	require.Equal(t, 0, service.Len())
}

func TestServiceKeepsHeldRetiredInstanceOpen(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""))
	service, tracker, clock := lifecycleService(t, store)
	old := service.snapshot.instances["a"]
	// A compiled workflow or a long-running stream still holds the instance.
	old.Acquire()
	err := service.Upsert(context.Background(), lifecycleDefinition("a", "changed", ""))
	require.NoError(t, err)

	*clock = clock.Add(5 * time.Minute)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	_, closed := tracker.counts()
	require.Equal(t, 0, closed)
	require.False(t, old.Closed())

	old.Release()
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	_, closed = tracker.counts()
	require.Equal(t, 1, closed)
}

func TestServiceViewsCarryTheGuardrailFlag(t *testing.T) {
	store := newTestStore(lifecycleDefinition("a", "one", ""))
	service, _, _ := lifecycleService(t, store)
	views := service.ListViews()
	require.Len(t, views, 1)
	require.True(t, views[0].Guardrail)

	types := service.TypeDefinitions()
	require.Len(t, types, 1)
	require.True(t, types[0].Guardrail)
}
