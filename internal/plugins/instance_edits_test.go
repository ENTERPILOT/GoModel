package plugins

import (
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

// editorPlugin is a mutating plugin whose configuration decides whether it
// actually edits content, the way presidio and string_replace do.
type editorPlugin struct {
	fakePlugin
	edits bool
}

func (e *editorPlugin) EditsContent() bool { return e.edits }

// A chained Responses request replays its stored history only for an instance
// that edits content, so the answer must come from the configured instance and
// fall back to the manifest for a plugin that does not say.
func TestInstanceEditsContent(t *testing.T) {
	tests := []struct {
		name     string
		instance *Instance
		want     bool
	}{
		{name: "nil instance"},
		{
			name:     "non-mutating plugin never edits",
			instance: &Instance{Plugin: &fakePlugin{name: "inspector"}},
		},
		{
			name:     "mutating plugin without ContentEditor is taken to edit",
			instance: &Instance{Manifest: pluginapi.Manifest{Mutates: true}, Plugin: &fakePlugin{name: "rewriter", mutates: true}},
			want:     true,
		},
		{
			name:     "configured editor that edits",
			instance: &Instance{Manifest: pluginapi.Manifest{Mutates: true}, Plugin: &editorPlugin{edits: true}},
			want:     true,
		},
		{
			name:     "configured editor that only flags",
			instance: &Instance{Manifest: pluginapi.Manifest{Mutates: true}, Plugin: &editorPlugin{}},
		},
		{
			name:     "non-mutating manifest wins over the plugin's answer",
			instance: &Instance{Plugin: &editorPlugin{edits: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.instance.EditsContent(); got != tt.want {
				t.Fatalf("EditsContent() = %v, want %v", got, tt.want)
			}
		})
	}
}
