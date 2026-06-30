package failover

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gomodel/config"
)

type memoryStore struct {
	rows map[string]Rule
}

func newMemoryStore(rows ...Rule) *memoryStore {
	store := &memoryStore{rows: make(map[string]Rule)}
	for _, row := range rows {
		store.rows[row.Source] = row.clone()
	}
	return store
}

func (s *memoryStore) List(context.Context) ([]Rule, error) {
	rows := make([]Rule, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row.clone())
	}
	return rows, nil
}

func (s *memoryStore) Get(_ context.Context, source string) (*Rule, error) {
	row, ok := s.rows[source]
	if !ok {
		return nil, ErrNotFound
	}
	clone := row.clone()
	return &clone, nil
}

func (s *memoryStore) Upsert(_ context.Context, rule Rule) error {
	s.rows[rule.Source] = rule.clone()
	return nil
}

func (s *memoryStore) Delete(_ context.Context, source string) error {
	if _, ok := s.rows[source]; !ok {
		return ErrNotFound
	}
	delete(s.rows, source)
	return nil
}

func (s *memoryStore) DeleteAll(context.Context) error {
	s.rows = make(map[string]Rule)
	return nil
}

func (s *memoryStore) Close() error { return nil }

// errGetStore returns a fixed error from Get, simulating a transient storage
// fault during the Upsert pre-read.
type errGetStore struct {
	*memoryStore
	getErr error
}

func (s *errGetStore) Get(context.Context, string) (*Rule, error) {
	return nil, s.getErr
}

func TestServiceUpsertPropagatesUnexpectedGetError(t *testing.T) {
	wantErr := errors.New("boom")
	store := &errGetStore{memoryStore: newMemoryStore(), getErr: wantErr}
	service, err := NewService(store, config.FallbackConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.Upsert(context.Background(), Rule{Source: "gpt-4o", Targets: []string{"azure/gpt-4o"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Upsert() error = %v, want it to wrap %v", err, wantErr)
	}
	if _, ok := store.rows["gpt-4o"]; ok {
		t.Fatalf("rule was written despite the failed pre-read")
	}
}

func TestServiceConfigRulesOverrideDashboardRules(t *testing.T) {
	store := newMemoryStore(Rule{
		Source:        "gpt-4o",
		Targets:       []string{"openrouter/gpt-4o"},
		Enabled:       true,
		ManagedSource: ManagedSourceDashboard,
	})
	service, err := NewService(store, config.FallbackConfig{
		Enabled: true,
		Manual: map[string][]string{
			"gpt-4o": {"azure/gpt-4o"},
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	got := service.Rules()["gpt-4o"]
	want := []string{"azure/gpt-4o"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Rules()[gpt-4o] = %v, want %v", got, want)
	}
	view, ok := service.Get("gpt-4o")
	if !ok || view.ManagedSource != ManagedSourceConfig {
		t.Fatalf("Get(gpt-4o) = %+v, %v; want config-managed rule", view, ok)
	}
}

// TestServiceRulesReuseCachedSnapshot guards the resolver hot path: Rules and
// Disabled are read on every request, so they must return the cached snapshot
// maps rather than rebuilding (and re-cloning every rule) on each call. A new
// snapshot is published only on Refresh.
func TestServiceRulesReuseCachedSnapshot(t *testing.T) {
	store := newMemoryStore(
		Rule{Source: "gpt-4o", Targets: []string{"azure/gpt-4o"}, Enabled: true, ManagedSource: ManagedSourceDashboard},
		Rule{Source: "gpt-4o-mini", Enabled: false, ManagedSource: ManagedSourceDashboard},
	)
	service, err := NewService(store, config.FallbackConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if a, b := service.Rules(), service.Rules(); reflect.ValueOf(a).Pointer() != reflect.ValueOf(b).Pointer() {
		t.Fatal("Rules() rebuilt the map; expected the cached snapshot reused across calls")
	}
	if a, b := service.Disabled(), service.Disabled(); reflect.ValueOf(a).Pointer() != reflect.ValueOf(b).Pointer() {
		t.Fatal("Disabled() rebuilt the map; expected the cached snapshot reused across calls")
	}

	before := service.Rules()
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if reflect.ValueOf(before).Pointer() == reflect.ValueOf(service.Rules()).Pointer() {
		t.Fatal("Refresh() did not publish a new snapshot")
	}
}
