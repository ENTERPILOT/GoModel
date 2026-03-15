package aliases

import (
	"context"
	"strings"
	"testing"

	"gomodel/internal/core"
)

type memoryStore struct {
	items map[string]Alias
}

func newMemoryStore(items ...Alias) *memoryStore {
	store := &memoryStore{items: make(map[string]Alias, len(items))}
	for _, item := range items {
		store.items[item.Name] = item
	}
	return store
}

func (s *memoryStore) List(_ context.Context) ([]Alias, error) {
	result := make([]Alias, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *memoryStore) Get(_ context.Context, name string) (*Alias, error) {
	item, ok := s.items[name]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *memoryStore) Upsert(_ context.Context, alias Alias) error {
	s.items[alias.Name] = alias
	return nil
}

func (s *memoryStore) Delete(_ context.Context, name string) error {
	if _, ok := s.items[name]; !ok {
		return ErrNotFound
	}
	delete(s.items, name)
	return nil
}

func (s *memoryStore) Close() error { return nil }

type testCatalog struct {
	providerTypes map[string]string
	models        map[string]core.Model
}

func newTestCatalog() *testCatalog {
	return &testCatalog{
		providerTypes: map[string]string{},
		models:        map[string]core.Model{},
	}
}

func (c *testCatalog) add(model string, providerType string, value core.Model) {
	c.providerTypes[model] = providerType
	c.models[model] = value
}

func (c *testCatalog) Supports(model string) bool {
	_, ok := c.models[model]
	return ok
}

func (c *testCatalog) GetProviderType(model string) string {
	return c.providerTypes[model]
}

func (c *testCatalog) LookupModel(model string) (*core.Model, bool) {
	value, ok := c.models[model]
	if !ok {
		return nil, false
	}
	copy := value
	return &copy, true
}

func TestServiceResolveAndExposeModels(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model", OwnedBy: "openai", Metadata: &core.ModelMetadata{DisplayName: "GPT-4o"}})

	service, err := NewService(newMemoryStore(Alias{Name: "smart", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	resolution, ok, err := service.Resolve("smart", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok {
		t.Fatal("Resolve() ok = false, want true")
	}
	if got := resolution.Resolved.QualifiedModel(); got != "gpt-4o" {
		t.Fatalf("resolved selector = %q, want gpt-4o", got)
	}
	if !service.Supports("smart") {
		t.Fatal("Supports(smart) = false, want true")
	}
	if got := service.GetProviderType("smart"); got != "openai" {
		t.Fatalf("GetProviderType(smart) = %q, want openai", got)
	}

	models := service.ExposedModels()
	if len(models) != 1 {
		t.Fatalf("len(ExposedModels()) = %d, want 1", len(models))
	}
	if models[0].ID != "smart" {
		t.Fatalf("alias model id = %q, want smart", models[0].ID)
	}
	if models[0].Metadata == nil || models[0].Metadata.DisplayName != "GPT-4o" {
		t.Fatalf("alias metadata = %#v, want copied target metadata", models[0].Metadata)
	}
}

func TestServiceUpsertRejectsAliasChainAndAllowsMasking(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})
	catalog.add("gpt-4o-mini", "openai", core.Model{ID: "gpt-4o-mini", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "front", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	err = service.Upsert(context.Background(), Alias{Name: "second", TargetModel: "front", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "refers to another alias") {
		t.Fatalf("Upsert(alias chain) error = %v, want alias-chain validation error", err)
	}

	err = service.Upsert(context.Background(), Alias{Name: "gpt-4o", TargetModel: "gpt-4o-mini", Enabled: true})
	if err != nil {
		t.Fatalf("Upsert(masking alias) error = %v, want nil", err)
	}

	resolution, ok, err := service.Resolve("gpt-4o", "")
	if err != nil {
		t.Fatalf("Resolve(masked model) error = %v", err)
	}
	if !ok {
		t.Fatal("Resolve(masked model) ok = false, want true")
	}
	if got := resolution.Resolved.QualifiedModel(); got != "gpt-4o-mini" {
		t.Fatalf("masked model resolved to %q, want gpt-4o-mini", got)
	}
}

func TestServiceSupportsQualifiedAliasNames(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "openai/smart", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	resolution, ok, err := service.Resolve("openai/smart", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !ok {
		t.Fatal("Resolve() ok = false, want true")
	}
	if resolution.Alias == nil || resolution.Alias.Name != "openai/smart" {
		t.Fatalf("resolved alias = %#v, want openai/smart", resolution.Alias)
	}
	if got := resolution.Resolved.QualifiedModel(); got != "gpt-4o" {
		t.Fatalf("resolved selector = %q, want gpt-4o", got)
	}
	if !service.Supports("openai/smart") {
		t.Fatal("Supports(openai/smart) = false, want true")
	}
	if got := service.GetProviderType("openai/smart"); got != "openai" {
		t.Fatalf("GetProviderType(openai/smart) = %q, want openai", got)
	}
}

func TestServiceUpsertRejectsQualifiedAliasChainsAndSelfTargets(t *testing.T) {
	catalog := newTestCatalog()
	catalog.add("gpt-4o", "openai", core.Model{ID: "gpt-4o", Object: "model"})
	catalog.add("gpt-4o-mini", "openai", core.Model{ID: "gpt-4o-mini", Object: "model"})

	service, err := NewService(newMemoryStore(Alias{Name: "openai/front", TargetModel: "gpt-4o", Enabled: true}), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	err = service.Upsert(context.Background(), Alias{Name: "qualified-second", TargetModel: "front", TargetProvider: "openai", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "refers to another alias") {
		t.Fatalf("Upsert(qualified alias chain) error = %v, want alias-chain validation error", err)
	}

	err = service.Upsert(context.Background(), Alias{Name: "openai/self", TargetModel: "self", TargetProvider: "openai", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "cannot target itself") {
		t.Fatalf("Upsert(qualified self target) error = %v, want self-target validation error", err)
	}
}
