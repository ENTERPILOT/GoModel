package pluginload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/pluginapi"
)

func TestOpen_Fixture(t *testing.T) {
	so := fixtureSO(t, "fixture")

	loaded, err := Open(so)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if loaded.Path != so {
		t.Errorf("Path = %q, want %q", loaded.Path, so)
	}
	if loaded.SingleInstance {
		t.Error("SingleInstance = true for a constructor symbol")
	}
	m := loaded.Manifest
	if m.Name != "fixture" || m.Version != "1.2.3" || len(m.Kinds) != 1 || m.Kinds[0] != pluginapi.KindPrompt {
		t.Errorf("Manifest = %+v", m)
	}
	if len(m.ConfigSchema) != 1 || m.ConfigSchema[0].Key != "greeting" {
		t.Errorf("ConfigSchema = %+v", m.ConfigSchema)
	}
	wantBuild := pluginapi.BuildInfo{GoVersion: "go-fixture", PluginAPIVersion: pluginapi.Version}
	if loaded.BuildInfo != wantBuild {
		t.Errorf("BuildInfo = %+v, want %+v", loaded.BuildInfo, wantBuild)
	}
	if m.BuiltWith != wantBuild {
		t.Errorf("Manifest.BuiltWith = %+v, want %+v (filled from the symbol)", m.BuiltWith, wantBuild)
	}

	a, b := loaded.Factory(), loaded.Factory()
	if a == b {
		t.Fatal("Factory() returned the same instance twice")
	}
	type serial interface{ Serial() int }
	sa, sb := a.(serial).Serial(), b.(serial).Serial()
	if sa == sb {
		t.Fatalf("instances share serial %d", sa)
	}

	hook, ok := a.(pluginapi.PromptHook)
	if !ok {
		t.Fatal("fixture does not implement PromptHook")
	}
	x := &pluginapi.Exchange{Prompt: &pluginapi.Prompt{Messages: []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "block me")}}}
	d, err := hook.OnPrompt(context.Background(), x)
	if err != nil || d.Action != pluginapi.ActionBlock {
		t.Fatalf("OnPrompt() = %+v, %v; want block", d, err)
	}
}

func TestLoad_Fixture(t *testing.T) {
	so := fixtureSO(t, "fixture")
	dir := filepath.Dir(so)
	sum, err := FileSHA256(so)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(config.PluginsConfig{
		SearchPaths: []string{dir},
		Load:        []config.PluginFileConfig{{File: filepath.Base(so), SHA256: sum}},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded) != 1 || loaded[0].Manifest.Name != "fixture" {
		t.Fatalf("Load() = %+v", loaded)
	}

	_, err = Load(config.PluginsConfig{Load: []config.PluginFileConfig{{File: so, SHA256: strings.Repeat("0", 64)}}})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("Load() with bad digest error = %v", err)
	}
}

func TestOpen_MissingSymbol(t *testing.T) {
	so := fixtureSO(t, "nosymbol")
	_, err := Open(so)
	if err == nil || !strings.Contains(err.Error(), "does not export GoModelPlugin") {
		t.Fatalf("Open(nosymbol) error = %v", err)
	}
}

func TestOpen_WrongSymbolType(t *testing.T) {
	so := fixtureSO(t, "badsymbol")
	_, err := Open(so)
	if err == nil || !strings.Contains(err.Error(), "symbol GoModelPlugin has type *int") {
		t.Fatalf("Open(badsymbol) error = %v", err)
	}
}

func TestOpen_NotASharedObject(t *testing.T) {
	skipUnlessLoadable(t)
	path := filepath.Join(t.TempDir(), "garbage.so")
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Open(garbage) error = %v", err)
	}
}
