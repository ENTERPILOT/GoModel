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

func TestPluginViewFromEntry_LabelsTheName(t *testing.T) {
	for name, want := range map[string]string{"header_edit": "Header Edit", "llm_judge": "LLM Judge", "cheapest_healthy": "Cheapest Healthy"} {
		entry := plugins.Entry{Name: name, Source: plugins.SourceBuiltin, Manifest: pluginapi.Manifest{Name: name}}
		if got := pluginViewFromEntry(entry).Label; got != want {
			t.Errorf("%s: label = %q, want %q", name, got, want)
		}
	}
}
