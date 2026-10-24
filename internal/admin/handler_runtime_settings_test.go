package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/echotest"
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
	require.NoError(t, err)

	t.Cleanup(func() { _ = backend.Close() })
	service, err := runtimesettings.New(context.Background(), backend, []ext.RuntimeSetting{setting})
	require.NoError(t, err)

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
	list := echotest.Decode[runtimeSettingsResponse](t, listRec)
	require.Len(t, list.Settings, 1)
	assert.Equal(t, "high", list.Settings[0].Value)

	updateRec := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"none"}`)
	require.Equal(t, http.StatusOK, updateRec.Code, updateRec.Body.String())
	assert.Equal(t, "none", setting.currentValue())
}

func TestRuntimeSettingManagedByEnvironmentIsReadOnly(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high", locked: true}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))

	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))
	rec := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"none"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "high", setting.currentValue(), "locked setting must not change")
}

func TestUpdateRuntimeSettingRejectsUnknownKeyAndInvalidValue(t *testing.T) {
	setting := &adminTestRuntimeSetting{value: "high"}
	h := NewHandler(nil, nil, WithRuntimeSettings(newAdminRuntimeSettingsService(t, setting)))
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	unknown := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/missing", `{"value":"none"}`)
	require.Equal(t, http.StatusNotFound, unknown.Code)
	assert.Contains(t, unknown.Body.String(), `"code":"runtime_setting_not_found"`)

	invalid := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"turbo"}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	assert.Equal(t, "high", setting.currentValue())
}

func TestRuntimeSettingsWithoutRegisteredExtensions(t *testing.T) {
	h := NewHandler(nil, nil)
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	list := runtimeSettingsRequest(e, http.MethodGet, "/admin/runtime/settings", "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	assert.Empty(t, echotest.Decode[runtimeSettingsResponse](t, list).Settings)

	update := runtimeSettingsRequest(e, http.MethodPut, "/admin/runtime/settings/pro.compression.level", `{"value":"high"}`)
	require.Equal(t, http.StatusServiceUnavailable, update.Code)
	assert.Contains(t, update.Body.String(), `"code":"feature_unavailable"`)
}
