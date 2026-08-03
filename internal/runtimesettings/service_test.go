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
	mu     sync.Mutex
	value  string
	locked bool
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
		return nil
	default:
		return fmt.Errorf("invalid level %q", value)
	}
}

func TestServicePersistsAndRestoresSetting(t *testing.T) {
	ctx := context.Background()
	backend, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "settings.db")})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer backend.Close()

	first := &testSetting{value: "high"}
	service, err := New(ctx, backend, []ext.RuntimeSetting{first})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	updated, err := service.Update(ctx, "pro.compression.level", "medium")
	if err != nil || updated.Value != "medium" {
		t.Fatalf("update = %+v, %v", updated, err)
	}

	restarted := &testSetting{value: "high"}
	reloaded, err := New(ctx, backend, []ext.RuntimeSetting{restarted})
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	if got := reloaded.List()[0].Value; got != "medium" {
		t.Fatalf("reloaded value = %q, want medium", got)
	}
}

func TestServiceEnvironmentLockIgnoresStoredValue(t *testing.T) {
	ctx := context.Background()
	backend, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "settings.db")})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer backend.Close()

	editable := &testSetting{value: "high"}
	service, err := New(ctx, backend, []ext.RuntimeSetting{editable})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
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
