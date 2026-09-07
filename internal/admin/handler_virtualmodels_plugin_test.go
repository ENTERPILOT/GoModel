package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/routeexample"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// newPluginVMHandler builds a handler whose virtual models service resolves
// the built-in cheapest_healthy routing strategy.
func newPluginVMHandler(t *testing.T) *Handler {
	t.Helper()
	pluginCatalog := plugins.NewCatalog()
	if err := pluginCatalog.Register(routeexample.New, plugins.SourceBuiltin); err != nil {
		t.Fatalf("Register: %v", err)
	}
	resolver := plugins.NewRouteResolver(pluginCatalog, plugins.HostDeps{})
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("openai/gpt-4o-mini", "openai")
	service := newVMService(t, catalog, newVMTestStore(), true)
	service.SetRouteResolver(resolver)
	return NewHandler(nil, nil, WithVirtualModels(service), WithPluginCatalog(pluginCatalog))
}

func putVirtualModel(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	return rec
}

func TestUpsertVirtualModelPluginStrategyRoundTrip(t *testing.T) {
	h := newPluginVMHandler(t)
	rec := putVirtualModel(t, h, `{"source":"smart","strategy":"plugin","strategy_plugin":"cheapest_healthy",
		"strategy_config":{"prefer":"fastest","max_error_rate":0.1},
		"targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var view virtualmodels.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if view.Strategy != "plugin" || view.StrategyPlugin != "cheapest_healthy" {
		t.Fatalf("view = %+v, want plugin strategy fields", view)
	}
	if want := map[string]any{"prefer": "fastest", "max_error_rate": 0.1}; !reflect.DeepEqual(view.StrategyConfig, want) {
		t.Fatalf("view.StrategyConfig = %v, want %v", view.StrategyConfig, want)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/virtual-models", nil)
	listRec := httptest.NewRecorder()
	if err := h.ListVirtualModels(echo.New().NewContext(listReq, listRec)); err != nil {
		t.Fatalf("ListVirtualModels() error = %v", err)
	}
	if !strings.Contains(listRec.Body.String(), `"strategy_plugin":"cheapest_healthy"`) {
		t.Fatalf("list body = %s, want strategy_plugin", listRec.Body.String())
	}
}

func TestUpsertVirtualModelPluginStrategyRejectsBadInput(t *testing.T) {
	cases := []struct{ name, body, wantErr string }{
		{
			name:    "missing plugin name",
			body:    `{"source":"smart","strategy":"plugin","targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`,
			wantErr: "strategy_plugin is required",
		},
		{
			name:    "unknown plugin",
			body:    `{"source":"smart","strategy":"plugin","strategy_plugin":"ghost","targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`,
			wantErr: `unknown routing-strategy plugin "ghost" (loaded: cheapest_healthy)`,
		},
		{
			name:    "unknown config key",
			body:    `{"source":"smart","strategy":"plugin","strategy_plugin":"cheapest_healthy","strategy_config":{"nope":1},"targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`,
			wantErr: `strategy plugin "cheapest_healthy": strategy_config: unknown config key "nope"`,
		},
		{
			name:    "bad option",
			body:    `{"source":"smart","strategy":"plugin","strategy_plugin":"cheapest_healthy","strategy_config":{"prefer":"slowest"},"targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`,
			wantErr: `config key "prefer"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := putVirtualModel(t, newPluginVMHandler(t), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body %s: %v", rec.Body.String(), err)
			}
			if !strings.Contains(body.Error.Message, tc.wantErr) {
				t.Fatalf("error message = %q, want containing %q", body.Error.Message, tc.wantErr)
			}
		})
	}
}
