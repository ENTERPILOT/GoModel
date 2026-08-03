package runtimesettings

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
)

type testSetting struct {
	mu      sync.Mutex
	value   string
	locked  bool
	applies int
}

func (s *testSetting) Descriptor() ext.SettingDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ext.SettingDescriptor{
		Key:    "pro.compression.level",
		Label:  "Prompt compression level",
		Value:  s.value,
		Locked: s.locked,
		Options: []ext.SettingOption{
			{Value: "none", Label: "None"},
			{Value: "medium", Label: "Medium"},
			{Value: "high", Label: "High"},
		},
	}
}

func (s *testSetting) Apply(value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch value {
	case "none", "medium", "high":
		s.value = value
		s.applies++
		return nil
	default:
		return fmt.Errorf("invalid level %q", value)
	}
}

func (s *testSetting) applyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applies
}

type stubStore struct {
	mu     sync.Mutex
	values map[string]string
	setErr error
}

func (s *stubStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.values[key]
	return value, found, nil
}

func (s *stubStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func testService(setting ext.RuntimeSetting, store Store) *Service {
	return &Service{
		store:    store,
		settings: map[string]ext.RuntimeSetting{"pro.compression.level": setting},
		order:    []string{"pro.compression.level"},
		rejected: make(map[string]string),
	}
}

func newTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	backend, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "settings.db")})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func TestServicePersistsAndRestoresSetting(t *testing.T) {
	ctx := context.Background()
	backend := newTestStorage(t)

	first := &testSetting{value: "high"}
	service, err := New(ctx, backend, []ext.RuntimeSetting{first})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	updated, err := service.Update(ctx, "pro.compression.level", "medium")
	if err != nil || updated.Value != "medium" {
		t.Fatalf("update = %+v, %v", updated, err)
	}

	restarted := &testSetting{value: "high"}
	reloaded, err := New(ctx, backend, []ext.RuntimeSetting{restarted})
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	if got := reloaded.List()[0].Value; got != "medium" {
		t.Fatalf("reloaded value = %q, want medium", got)
	}
}

func TestServiceEnvironmentLockIgnoresStoredValue(t *testing.T) {
	ctx := context.Background()
	backend := newTestStorage(t)

	editable := &testSetting{value: "high"}
	service, err := New(ctx, backend, []ext.RuntimeSetting{editable})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	editableService := service
	t.Cleanup(func() { _ = editableService.Close() })
	if _, err := service.Update(ctx, "pro.compression.level", "medium"); err != nil {
		t.Fatalf("persist medium: %v", err)
	}

	locked := &testSetting{value: "none", locked: true}
	service, err = New(ctx, backend, []ext.RuntimeSetting{locked})
	if err != nil {
		t.Fatalf("reload locked service: %v", err)
	}
	if got := service.List()[0].Value; got != "none" {
		t.Fatalf("locked value = %q, want none", got)
	}
	if _, err := service.Update(ctx, "pro.compression.level", "high"); err != ErrLocked {
		t.Fatalf("locked update error = %v, want ErrLocked", err)
	}
}

func TestServiceUpdateRollsBackWhenPersistenceFails(t *testing.T) {
	persistErr := fmt.Errorf("database unavailable")
	setting := &testSetting{value: "high"}
	service := testService(setting, &stubStore{setErr: persistErr})

	_, err := service.Update(context.Background(), "pro.compression.level", "medium")
	if err != persistErr {
		t.Fatalf("update error = %v, want %v", err, persistErr)
	}
	if got := setting.Descriptor().Value; got != "high" {
		t.Fatalf("value after rollback = %q, want high", got)
	}
	if applies := setting.applyCount(); applies != 2 {
		t.Fatalf("Apply calls = %d, want update and rollback", applies)
	}
}

func TestServiceSynchronizesChangedValuesAcrossInstances(t *testing.T) {
	ctx := context.Background()
	backend := newTestStorage(t)

	first := &testSetting{value: "high"}
	second := &testSetting{value: "high"}
	writer, err := New(ctx, backend, []ext.RuntimeSetting{first})
	if err != nil {
		t.Fatalf("create writer service: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	peer, err := New(ctx, backend, []ext.RuntimeSetting{second})
	if err != nil {
		t.Fatalf("create peer service: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	if _, err := writer.Update(ctx, "pro.compression.level", "medium"); err != nil {
		t.Fatalf("writer update: %v", err)
	}
	if err := peer.sync(ctx); err != nil {
		t.Fatalf("peer sync: %v", err)
	}
	if got := second.Descriptor().Value; got != "medium" {
		t.Fatalf("peer value = %q, want medium", got)
	}
	applies := second.applyCount()
	if err := peer.sync(ctx); err != nil {
		t.Fatalf("unchanged peer sync: %v", err)
	}
	if after := second.applyCount(); after != applies {
		t.Fatalf("unchanged sync called Apply: before=%d after=%d", applies, after)
	}
}

func TestServiceRejectsEditableSettingWithoutOptions(t *testing.T) {
	backend := newTestStorage(t)

	empty := &optionlessSetting{testSetting: &testSetting{value: "high"}}
	if _, err := New(context.Background(), backend, []ext.RuntimeSetting{empty}); err == nil {
		t.Fatal("expected registration without options to fail")
	}
}

type optionlessSetting struct{ testSetting *testSetting }

func (s *optionlessSetting) Descriptor() ext.SettingDescriptor {
	descriptor := s.testSetting.Descriptor()
	descriptor.Options = nil
	return descriptor
}

func (s *optionlessSetting) Apply(value string) error { return s.testSetting.Apply(value) }

func TestNilServiceUpdateReturnsNotFound(t *testing.T) {
	var service *Service
	if _, err := service.Update(context.Background(), "missing", "high"); err != ErrNotFound {
		t.Fatalf("nil service update error = %v, want ErrNotFound", err)
	}
}
