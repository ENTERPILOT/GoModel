package failover

import (
	"context"
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
