package inferencehook_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/inferencehook"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
)

// A security server that cannot deliver a verdict must not decide the
// request by itself: the instance fail mode does, and it is closed unless
// the operator opens it.
func TestFailModeDecidesAFailedHook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	catalog := plugins.NewCatalog()
	require.NoError(t, catalog.Register(inferencehook.New, plugins.SourceBuiltin))
	entry, ok := catalog.Lookup(inferencehook.Name)
	require.True(t, ok)
	secret := base64.StdEncoding.EncodeToString([]byte("a signing key for the tests only"))
	config, err := json.Marshal(map[string]string{"endpoint_url": srv.URL, "signing_secret": secret})
	require.NoError(t, err)

	tests := []struct {
		name     string
		failMode plugins.FailMode
		wantErr  bool
	}{
		{"default is closed", "", true},
		{"closed", plugins.FailClosed, true},
		{"open", plugins.FailOpen, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst, err := plugins.NewInstance(context.Background(), entry, plugins.InstanceSpec{Name: "dlp", Config: config, FailMode: tt.failMode}, plugintest.NewHost())
			require.NoError(t, err)
			chain, err := plugins.BuildChain(pluginapi.KindPrompt, []plugins.Ref{{Instance: inst}})
			require.NoError(t, err)

			x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "hi")), nil)
			outcome, err := chain.RunPrompt(context.Background(), x)
			if tt.wantErr {
				var failure *plugins.PluginError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, "dlp", failure.Instance)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, pluginapi.ActionAllow, outcome.Decision.Action)
			require.Len(t, outcome.Records, 1)
			assert.ErrorContains(t, outcome.Records[0].Err, "returned HTTP 503", "the fail-open failure is still recorded")
		})
	}
}
