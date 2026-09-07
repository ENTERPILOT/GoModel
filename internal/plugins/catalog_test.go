package plugins

import (
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
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
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Register() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			entry, ok := catalog.Lookup(tt.plugin.Manifest().Name)
			if !ok {
				t.Fatal("Lookup() = false, want entry")
			}
			if len(entry.Kinds) != len(tt.kinds) {
				t.Fatalf("Kinds = %v, want %v", entry.Kinds, tt.kinds)
			}
			for i := range tt.kinds {
				if entry.Kinds[i] != tt.kinds[i] {
					t.Fatalf("Kinds = %v, want %v", entry.Kinds, tt.kinds)
				}
			}
			if entry.Health != "ok" || entry.Source != SourceBuiltin {
				t.Fatalf("entry = %+v, want ok/builtin", entry)
			}
		})
	}
}

func TestCatalogDuplicateAndFailed(t *testing.T) {
	catalog := NewCatalog()
	if err := catalog.Register(factoryOf(&fakePlugin{name: "a"}), SourceBuiltin); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(factoryOf(&fakePlugin{name: "a"}), SourceRegistered); err == nil {
		t.Fatal("duplicate Register() error = nil")
	}
	catalog.RegisterFailed("broken", Source("/tmp/broken.so"), errFake)
	if _, ok := catalog.Lookup("broken"); ok {
		t.Fatal("Lookup(broken) = true, want false for a failed entry")
	}
	if names := catalog.Names(); len(names) != 1 || names[0] != "a" {
		t.Fatalf("Names() = %v, want [a]", names)
	}
	entries := catalog.Entries()
	if len(entries) != 2 || entries[1].Health != "error" || entries[1].Err == nil {
		t.Fatalf("Entries() = %+v, want a and failed broken", entries)
	}
	if catalog.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", catalog.Len())
	}
}

func TestCatalogRegisterRecoversPanic(t *testing.T) {
	catalog := NewCatalog()
	err := catalog.Register(func() pluginapi.Plugin { panic("boom") }, SourceBuiltin)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("Register() error = %v, want panic error", err)
	}
}
