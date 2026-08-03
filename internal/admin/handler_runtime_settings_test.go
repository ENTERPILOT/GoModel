package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
)

type adminTestRuntimeSetting struct {
	value  string
	locked bool
}

func (s *adminTestRuntimeSetting) Descriptor() ext.SettingDescriptor {
	return ext.SettingDescriptor{
		Key:    "pro.compression.level",
		Label:  "Prompt compression level",
		Value:  s.value,
		Locked: s.locked,
		Options: []ext.SettingOption{
			{Value: "none", Label: "None"},
			{Value: "high", Label: "High"},
		},
	}
}

func (s *adminTestRuntimeSetting) Apply(value string) error {
	if value != "none" && value != "high" {
		return fmt.Errorf("invalid level")
	}
	s.value = value
	return nil
}

func newAdminRuntimeSettingsService(t *testing.T, setting ext.RuntimeSetting) *runtimesettings.Service {
	t.Helper()
	backend, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "admin-settings.db")})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	service, err := runtimesettings.New(context.Background(), backend, []ext.RuntimeSetting{setting})
	if err != nil {
		t.Fatalf("create runtime settings service: %v", err)
	}
	return service
}

func TestRuntimeSettingsListAndUpdate(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high"}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))

	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))
	listReq := httptest.NewRequest(http.MethodGet, "/admin/runtime/settings", nil)
	listRec := httptest.NewRecorder()
	e.ServeHTTP(listRec, listReq)
	var list runtimeSettingsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Settings) != 1 || list.Settings[0].Value != "high" {
		t.Fatalf("settings = %+v", list.Settings)
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/admin/runtime/settings/pro.compression.level", strings.NewReader(`{"value":"none"}`))
	updateReq.Header.Set("Content-Type", "application/json")
	updateRec := httptest.NewRecorder()
	e.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK || setting.value != "none" {
		t.Fatalf("update status=%d value=%q body=%s", updateRec.Code, setting.value, updateRec.Body.String())
	}
}

func TestRuntimeSettingManagedByEnvironmentIsReadOnly(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high", locked: true}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))

	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))
	req := httptest.NewRequest(http.MethodPut, "/admin/runtime/settings/pro.compression.level", strings.NewReader(`{"value":"none"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || setting.value != "high" {
		t.Fatalf("locked update status=%d value=%q body=%s", rec.Code, setting.value, rec.Body.String())
	}
}
