package admin

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
)

func TestPluginViewFromEntry_ReportsGuardrail(t *testing.T) {
	for _, tt := range []struct {
		name string
		want bool
	}{{"judge", true}, {"rewriter", false}} {
		entry := plugins.Entry{Name: tt.name, Source: plugins.SourceBuiltin, Manifest: pluginapi.Manifest{Name: tt.name, Guardrail: tt.want}}
		got := pluginViewFromEntry(entry).Guardrail
		assert.Equal(t, tt.want, got, tt.name)
	}
}

func TestPluginViewFromEntry_LabelsTheName(t *testing.T) {
	for name, want := range map[string]string{"header_edit": "Header Edit", "llm_judge": "LLM Judge", "cheapest_healthy": "Cheapest Healthy"} {
		entry := plugins.Entry{Name: name, Source: plugins.SourceBuiltin, Manifest: pluginapi.Manifest{Name: name}}
		got := pluginViewFromEntry(entry).Label
		assert.Equal(t, want, got)
	}
}
