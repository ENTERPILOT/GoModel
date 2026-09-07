package admin

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

func TestPluginViewFromEntry_ReportsGuardrail(t *testing.T) {
	for _, tt := range []struct {
		name string
		want bool
	}{{"judge", true}, {"rewriter", false}} {
		entry := plugins.Entry{Name: tt.name, Source: plugins.SourceBuiltin, Manifest: pluginapi.Manifest{Name: tt.name, Guardrail: tt.want}}
		if got := pluginViewFromEntry(entry).Guardrail; got != tt.want {
			t.Errorf("%s: guardrail = %v, want %v", tt.name, got, tt.want)
		}
	}
}
