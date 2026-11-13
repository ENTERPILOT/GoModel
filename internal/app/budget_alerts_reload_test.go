package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/providers"
)

// storeAwareSink is an alert sink that keeps state in the shared store.
type storeAwareSink struct {
	events chan ext.AlertEvent

	mu     sync.Mutex
	stores []ext.SharedStore
}

func (*storeAwareSink) Name() string { return "store-aware-sink" }

func (s *storeAwareSink) SendAlert(_ context.Context, event ext.AlertEvent) error {
	s.events <- event
	return nil
}

func (s *storeAwareSink) SetSharedStore(store ext.SharedStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stores = append(s.stores, store)
}

func (s *storeAwareSink) store() (ext.SharedStore, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stores) == 0 {
		return nil, 0
	}
	return s.stores[len(s.stores)-1], len(s.stores)
}

// A sink is registered once and outlives reloads: every generation must hand
// it its shared store, and the newest generation's dispatcher must deliver to
// it after the old one shut down.
func TestAlertSinkFollowsReloads(t *testing.T) {
	ctx := context.Background()
	t.Setenv("SET_BUDGET_TEAM", `[{"period":"monthly","amount":10,"alert_thresholds":[50]}]`)
	loaded := loadSharedStoreTestConfig(t)
	sink := &storeAwareSink{events: make(chan ext.AlertEvent, 4)}
	registry := &ext.Registry{}
	registry.RegisterAlertSink(sink)

	first, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory(), Extensions: registry})
	require.NoError(t, err)
	firstStore, calls := sink.store()
	require.NotNil(t, firstStore, "an alert sink alone gets the shared store")
	assert.Equal(t, 1, calls)
	require.NotNil(t, first.budgetAlerts)

	reloaded, err := config.Load()
	require.NoError(t, err)
	second, err := New(ctx, Config{AppConfig: reloaded, Factory: providers.NewProviderFactory(), Extensions: registry, Replaces: first})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Shutdown(ctx) })
	secondStore, calls := sink.store()
	assert.Equal(t, 2, calls)
	require.NotSame(t, firstStore, secondStore, "the reload rebinds the sink")
	require.NotNil(t, second.budgetAlerts)
	require.NotSame(t, first.budgetAlerts.dispatcher, second.budgetAlerts.dispatcher)

	require.NoError(t, first.Shutdown(ctx))
	require.NoError(t, secondStore.Put(ctx, "test.alerts", "k", []byte("v"), time.Now().Add(time.Hour)),
		"the old generation's shutdown leaves the sink's store working")

	writeAlertTestUsage(t, second, "/team/app", 6)
	require.NoError(t, second.budgetAlerts.alerter.Evaluate(ctx))
	select {
	case event := <-sink.events:
		assert.Equal(t, "/team", event.Budget.Subject)
		assert.Equal(t, 50, event.ThresholdPercent)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the reloaded generation did not deliver to the sink")
	}
	require.NoError(t, second.budgetAlerts.alerter.Evaluate(ctx))
	require.NoError(t, second.Shutdown(ctx))
	assert.Empty(t, sink.events, "the alert is delivered once")
}
