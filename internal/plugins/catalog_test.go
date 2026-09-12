package plugins

import (
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
)

func TestCatalogRegister(t *testing.T) {
	tests := []struct {
		name    string
		plugin  pluginapi.Plugin
		wantErr string
		kinds   []pluginapi.Kind
	}{
		{
			name:   "declared kinds are recorded",
			plugin: &fakePlugin{name: "ok", kinds: []pluginapi.Kind{pluginapi.KindPrompt}},
			kinds:  []pluginapi.Kind{pluginapi.KindPrompt},
		},
		{
			name:   "undeclared kinds fall back to implemented hooks",
			plugin: &fakePlugin{name: "implicit"},
			kinds:  []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		},
		{
			name:    "declared kind without hook is rejected",
			plugin:  &promptOnly{fakePlugin{name: "liar", kinds: []pluginapi.Kind{pluginapi.KindRoute}}},
			wantErr: "does not implement",
		},
		{
			name:    "empty name is rejected",
			plugin:  &fakePlugin{name: "  "},
			wantErr: "name is required",
		},
		{
			name:    "duplicate field keys are rejected",
			plugin:  &fakePlugin{name: "dup", schema: []pluginapi.Field{{Key: "a"}, {Key: "a"}}},
			wantErr: "twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := NewCatalog()
			err := catalog.Register(factoryOf(tt.plugin), SourceBuiltin)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			entry, ok := catalog.Lookup(tt.plugin.Manifest().Name)
			require.True(t, ok)
			require.Len(t, entry.Kinds, len(tt.kinds))

			for i := range tt.kinds {
				require.Equal(t, tt.kinds[i], entry.Kinds[i])
			}
			require.Equal(t, "ok", entry.Health)
			require.Equal(t, SourceBuiltin, entry.Source, "entry = %+v, want ok/builtin", entry)
		})
	}
}

func TestCatalogDuplicateAndFailed(t *testing.T) {
	catalog := NewCatalog()
	err := catalog.Register(factoryOf(&fakePlugin{name: "a"}), SourceBuiltin)
	require.NoError(t, err)
	require.Error(t, catalog.Register(factoryOf(&fakePlugin{name: "a"}), SourceRegistered))

	catalog.RegisterFailed("broken", Source("/tmp/broken.so"), errFake)
	_, ok := catalog.Lookup("broken")
	require.False(t, ok)
	names := catalog.Names()
	require.Len(t, names, 1)
	require.Equal(t, "a", names[0])

	entries := catalog.Entries()
	require.Len(t, entries, 2)
	require.Equal(t, "error", entries[1].Health)
	require.Error(t, entries[1].Err)
	require.Equal(t, 1, catalog.Len())
}

func TestCatalogRegisterRecoversPanic(t *testing.T) {
	catalog := NewCatalog()
	err := catalog.Register(func() pluginapi.Plugin { panic("boom") }, SourceBuiltin)
	require.ErrorContains(t, err, "panicked")
}
