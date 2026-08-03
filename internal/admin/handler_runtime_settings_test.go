package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
)

type adminTestRuntimeSetting struct {
	mu     sync.Mutex
	value  string
	locked bool
}

func (s *adminTestRuntimeSetting) Descriptor() ext.SettingDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if value != "none" && value != "high" {
		return fmt.Errorf("invalid level")
	}
	s.value = value
	return nil
}

func (s *adminTestRuntimeSetting) currentValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
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
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func runtimeSettingsRequest(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestRuntimeSettingsListAndUpdate(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high"}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))

	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))
	listRec := runtimeSettingsRequest(e, http.MethodGet, "/admin/runtime/settings", "")
	var list runtimeSettingsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Settings) != 1 || list.Settings[0].Value != "high" {
		t.Fatalf("settings = %+v", list.Settings)
	}

	updateRec := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"none"}`)
	if updateRec.Code != http.StatusOK || setting.currentValue() != "none" {
		t.Fatalf("update status=%d value=%q body=%s", updateRec.Code, setting.currentValue(), updateRec.Body.String())
	}
}

func TestRuntimeSettingManagedByEnvironmentIsReadOnly(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high", locked: true}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))

	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))
	rec := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"none"}`)
	if rec.Code != http.StatusBadRequest || setting.currentValue() != "high" {
		t.Fatalf("locked update status=%d value=%q body=%s", rec.Code, setting.currentValue(), rec.Body.String())
	}
}

func TestUpdateRuntimeSettingRejectsUnknownKeyAndInvalidValue(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high"}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	unknown := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/missing", `{"value":"none"}`)
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Body.String(), `"code":"runtime_setting_not_found"`) {
		t.Fatalf("unknown update status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	invalid := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"turbo"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid update status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	if setting.currentValue() != "high" {
		t.Fatalf("rejected updates changed value to %q", setting.currentValue())
	}
}

func TestRuntimeSettingsWithoutRegisteredExtensions(t *testing.T) {
	h := NewHandler(nil, nil)
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	list := runtimeSettingsRequest(e, http.MethodGet, "/admin/runtime/settings", "")
	var response runtimeSettingsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode empty list: %v", err)
	}
	if list.Code != http.StatusOK || len(response.Settings) != 0 {
		t.Fatalf("empty list status=%d body=%s", list.Code, list.Body.String())
	}
	update := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"high"}`)
	if update.Code != http.StatusServiceUnavailable || !strings.Contains(update.Body.String(), `"code":"feature_unavailable"`) {
		t.Fatalf("unavailable update status=%d body=%s", update.Code, update.Body.String())
	}
}
