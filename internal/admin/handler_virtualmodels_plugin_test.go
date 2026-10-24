package admin

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/routeexample"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// newPluginVMHandler builds a handler whose virtual models service resolves
// the built-in cheapest_healthy routing strategy.
func newPluginVMHandler(t *testing.T) *Handler {
	t.Helper()
	pluginCatalog := plugins.NewCatalog()
	err := pluginCatalog.Register(routeexample.New, plugins.SourceBuiltin)
	require.NoError(t, err)

	resolver := plugins.NewRouteResolver(pluginCatalog, plugins.HostDeps{})
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("openai/gpt-4o-mini", "openai")
	service := newVMService(t, catalog, newVMTestStore(), true)
	service.SetRouteResolver(resolver)
	return NewHandler(nil, nil, WithVirtualModels(service), WithPluginCatalog(pluginCatalog))
}

func TestUpsertVirtualModelPluginStrategyRoundTrip(t *testing.T) {
	h := newPluginVMHandler(t)
	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","strategy":"plugin","strategy_plugin":"cheapest_healthy",
		"strategy_config":{"prefer":"fastest","max_error_rate":0.1},
		"targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.Equal(t, "plugin", view.Strategy)
	assert.Equal(t, "cheapest_healthy", view.StrategyPlugin)
	assert.Equal(t, map[string]any{"prefer": "fastest", "max_error_rate": 0.1}, view.StrategyConfig)

	listCtx, listRec := echotest.Get(t, "/admin/virtual-models")
	err = h.ListVirtualModels(listCtx)
	require.NoError(t, err)
	assert.Contains(t, listRec.Body.String(), `"strategy_plugin":"cheapest_healthy"`)
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
			c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", tc.body)
			err := newPluginVMHandler(t).UpsertVirtualModel(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			body := echotest.Decode[workflowErrorEnvelope](t, rec)
			assert.Contains(t, body.Error.Message, tc.wantErr)
		})
	}
}
