package runtimesettings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/stretchr/testify/assert"
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
	mu        sync.Mutex
	values    map[string]string
	getErrs   map[string]error
	getAllErr error
	setErr    error
	gets      int
	getAlls   int
	// stalled makes GetAll hang until its context ends, like a query on a
	// connection the database stopped answering.
	stalled bool
	// afterRead runs in GetAll once the values are read, without s.mu.
	afterRead func()
}

func (s *stubStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if err := s.getErrs[key]; err != nil {
		return "", false, err
	}
	value, found := s.values[key]
	return value, found, nil
}

func (s *stubStore) GetAll(ctx context.Context) (map[string]string, error) {
	s.mu.Lock()
	s.getAlls++
	if s.stalled {
		s.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	values, err := maps.Clone(s.values), s.getAllErr
	afterRead := s.afterRead
	s.mu.Unlock()
	if afterRead != nil {
		afterRead()
	}
	if err != nil {
		return nil, err
	}
	return values, nil
}

func (s *stubStore) calls() (gets, getAlls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.getAlls
}

func (s *stubStore) Replace(_ context.Context, key, old, value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return "", s.setErr
	}
	if s.values[key] == old {
		s.values[key] = value
	}
	return s.values[key], nil
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
	require.ErrorIs(t, err, ErrLocked)
}

func TestServiceUpdateRollsBackWhenPersistenceFails(t *testing.T) {
	persistErr := fmt.Errorf("database unavailable")
	setting := &testSetting{value: "high"}
	service := testService(setting, &stubStore{setErr: persistErr})

	_, err := service.Update(context.Background(), "pro.compression.level", "medium")
	require.ErrorIs(t, err, persistErr)
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
	applyErr := errors.New("apply failed")
	applyFailure := &testSetting{key: "a.apply", value: "high", applyErr: applyErr}
	success := &testSetting{key: "b.success", value: "high"}
	service := &Service{
		store: &stubStore{
			values: map[string]string{"a.apply": "medium", "b.success": "medium"},
		},
		settings: map[string]ext.RuntimeSetting{
			"a.apply":   applyFailure,
			"b.success": success,
		},
		order:    []string{"a.apply", "b.success"},
		rejected: make(map[string]string),
	}

	err := service.sync(context.Background())
	require.ErrorIs(t, err, applyErr)
	assert.Contains(t, err.Error(), `"a.apply"`)
	assert.Equal(t, "medium", success.Descriptor().Value)
}

func TestServiceSyncReadsEverySettingInOneQuery(t *testing.T) {
	tests := []struct {
		name     string
		settings int
	}{
		{name: "one setting", settings: 1},
		{name: "many settings", settings: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubStore{values: map[string]string{}}
			service := &Service{
				store:    store,
				settings: make(map[string]ext.RuntimeSetting),
				rejected: make(map[string]string),
			}
			var settings []*testSetting
			for i := range tt.settings {
				key := fmt.Sprintf("setting.%02d", i)
				setting := &testSetting{key: key, value: "high"}
				settings = append(settings, setting)
				service.settings[key] = setting
				service.order = append(service.order, key)
				store.values[key] = "medium"
			}

			require.NoError(t, service.sync(context.Background()))
			gets, getAlls := store.calls()
			assert.Zero(t, gets)
			assert.Equal(t, 1, getAlls)
			for _, setting := range settings {
				assert.Equal(t, "medium", setting.Descriptor().Value)
			}
		})
	}
}

func TestServiceSyncReadFailureAppliesNothing(t *testing.T) {
	readErr := errors.New("read failed")
	setting := &testSetting{value: "high"}
	service := testService(setting, &stubStore{
		values:    map[string]string{"pro.compression.level": "medium"},
		getAllErr: readErr,
	})

	err := service.sync(context.Background())
	require.ErrorIs(t, err, readErr)
	assert.Equal(t, "high", setting.Descriptor().Value)
}

// A value read before a concurrent Update must not undo that Update.
func TestServiceSyncDropsReadOvertakenByUpdate(t *testing.T) {
	ctx := context.Background()
	setting := &testSetting{value: "high"}
	store := &stubStore{values: map[string]string{"pro.compression.level": "medium"}}
	service := testService(setting, store)
	store.afterRead = func() {
		_, err := service.Update(ctx, "pro.compression.level", "none")
		assert.NoError(t, err)
	}

	require.NoError(t, service.sync(ctx))
	assert.Equal(t, "none", setting.Descriptor().Value, "the stale read is dropped")

	store.mu.Lock()
	store.afterRead = nil
	store.mu.Unlock()
	require.NoError(t, service.sync(ctx))
	assert.Equal(t, "none", setting.Descriptor().Value)
}

func TestServiceSyncBoundsStalledRead(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	synctest.Test(t, func(t *testing.T) {
		setting := &testSetting{value: "high"}
		store := &stubStore{values: map[string]string{"pro.compression.level": "medium"}, stalled: true}
		service := testService(setting, store)

		started := time.Now()
		err := service.sync(context.Background())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, syncTimeout, time.Since(started), "a stalled read ends at its deadline")

		service.startSync()
		time.Sleep(3 * (syncInterval + syncTimeout))
		_, err = service.Update(context.Background(), "pro.compression.level", "none")
		require.NoError(t, err, "a stalled read does not block Update")
		_, getAlls := store.calls()
		assert.GreaterOrEqual(t, getAlls, 3, "the loop keeps ticking while reads stall")

		store.mu.Lock()
		store.stalled = false
		store.values["pro.compression.level"] = "medium"
		store.mu.Unlock()
		time.Sleep(syncTimeout + 2*syncInterval)
		synctest.Wait()
		assert.Equal(t, "medium", setting.Descriptor().Value, "values apply once the database answers again")
		require.NoError(t, service.Close())
	})
	assert.Equal(t, 1, strings.Count(logs.String(), "failed to synchronize runtime settings"), "a persistent failure is logged once")
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
	require.ErrorIs(t, err, ErrNotFound)
}
