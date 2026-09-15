package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/stretchr/testify/require"
)

type testSetting struct {
	mu       sync.Mutex
	key      string
	value    string
	locked   bool
	applies  int
	applyErr error
}

func (s *testSetting) Descriptor() ext.SettingDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.key
	if key == "" {
		key = "pro.compression.level"
	}
	return ext.SettingDescriptor{
		Key:    key,
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
	if s.applyErr != nil {
		return s.applyErr
	}
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
	mu      sync.Mutex
	values  map[string]string
	getErrs map[string]error
	setErr  error
}

func (s *stubStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.getErrs[key]; err != nil {
		return "", false, err
	}
	value, found := s.values[key]
	return value, found, nil
}

func (s *stubStore) SetDefault(_ context.Context, key, value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.getErrs[key]; err != nil {
		return "", err
	}
	if stored, found := s.values[key]; found {
		return stored, nil
	}
	if s.setErr != nil {
		return "", s.setErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return value, nil
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
	require.NoError(t, err)

	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func TestServicePersistsAndRestoresSetting(t *testing.T) {
	ctx := context.Background()
	backend := newTestStorage(t)

	first := &testSetting{value: "high"}
	service, err := New(ctx, backend, []ext.RuntimeSetting{first})
	require.NoError(t, err)

	t.Cleanup(func() { _ = service.Close() })
	updated, err := service.Update(ctx, "pro.compression.level", "medium")
	require.NoError(t, err)
	require.Equal(t, "medium", updated.Value, "update = %+v, %v", updated, err)

	restarted := &testSetting{value: "high"}
	reloaded, err := New(ctx, backend, []ext.RuntimeSetting{restarted})
	require.NoError(t, err)

	t.Cleanup(func() { _ = reloaded.Close() })
	got := reloaded.List()[0].Value
	require.Equal(t, "medium", got)
}

func TestServiceEnvironmentLockIgnoresStoredValue(t *testing.T) {
	ctx := context.Background()
	backend := newTestStorage(t)

	editable := &testSetting{value: "high"}
	service, err := New(ctx, backend, []ext.RuntimeSetting{editable})
	require.NoError(t, err)

	editableService := service
	t.Cleanup(func() { _ = editableService.Close() })
	_, err = service.Update(ctx, "pro.compression.level", "medium")
	require.NoError(t, err)

	locked := &testSetting{value: "none", locked: true}
	service, err = New(ctx, backend, []ext.RuntimeSetting{locked})
	require.NoError(t, err)
	got := service.List()[0].Value
	require.Equal(t, "none", got)
	_, err = service.Update(ctx, "pro.compression.level", "high")
	require.Equal(t, ErrLocked, err)
}

func TestServiceUpdateRollsBackWhenPersistenceFails(t *testing.T) {
	persistErr := fmt.Errorf("database unavailable")
	setting := &testSetting{value: "high"}
	service := testService(setting, &stubStore{setErr: persistErr})

	_, err := service.Update(context.Background(), "pro.compression.level", "medium")
	require.Equal(t, persistErr, err)
	got := setting.Descriptor().Value
	require.Equal(t, "high", got)
	applies := setting.applyCount()
	require.Equal(t, 2, applies)
}

func TestServiceSynchronizesChangedValuesAcrossInstances(t *testing.T) {
	ctx := context.Background()
	backend := newTestStorage(t)

	first := &testSetting{value: "high"}
	second := &testSetting{value: "high"}
	writer, err := New(ctx, backend, []ext.RuntimeSetting{first})
	require.NoError(t, err)

	t.Cleanup(func() { _ = writer.Close() })
	peer, err := New(ctx, backend, []ext.RuntimeSetting{second})
	require.NoError(t, err)

	t.Cleanup(func() { _ = peer.Close() })
	_, err = writer.Update(ctx, "pro.compression.level", "medium")
	require.NoError(t, err)
	err = peer.sync(ctx)
	require.NoError(t, err)
	got := second.Descriptor().Value
	require.Equal(t, "medium", got)

	applies := second.applyCount()
	err = peer.sync(ctx)
	require.NoError(t, err)
	after := second.applyCount()
	require.Equal(t, applies, after)
}

func TestServiceSyncContinuesAfterSettingFailures(t *testing.T) {
	getErr := errors.New("read failed")
	applyErr := errors.New("apply failed")
	readFailure := &testSetting{key: "a.read", value: "high"}
	applyFailure := &testSetting{key: "b.apply", value: "high", applyErr: applyErr}
	success := &testSetting{key: "c.success", value: "high"}
	service := &Service{
		store: &stubStore{
			values:  map[string]string{"b.apply": "medium", "c.success": "medium"},
			getErrs: map[string]error{"a.read": getErr},
		},
		settings: map[string]ext.RuntimeSetting{
			"a.read":    readFailure,
			"b.apply":   applyFailure,
			"c.success": success,
		},
		order:    []string{"a.read", "b.apply", "c.success"},
		rejected: make(map[string]string),
	}

	err := service.sync(context.Background())
	require.ErrorIs(t, err, getErr)
	require.ErrorIs(t, err, applyErr)
	got := err.Error()
	require.Contains(t, got, `"a.read"`)
	require.Contains(t, got, `"b.apply"`)
	got = success.Descriptor().Value
	require.Equal(t, "medium", got)
}

func TestServiceRejectsEditableSettingWithoutOptions(t *testing.T) {
	backend := newTestStorage(t)

	empty := &optionlessSetting{testSetting: &testSetting{value: "high"}}
	_, err := New(context.Background(), backend, []ext.RuntimeSetting{empty})
	require.Error(t, err)
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
	_, err := service.Update(context.Background(), "missing", "high")
	require.Equal(t, ErrNotFound, err)
}
