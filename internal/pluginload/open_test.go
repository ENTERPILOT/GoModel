package pluginload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_Fixture(t *testing.T) {
	so := fixtureSO(t, "fixture")

	loaded, err := Open(so)
	require.NoError(t, err)
	assert.Equal(t, so, loaded.Path)
	assert.False(t, loaded.SingleInstance)

	m := loaded.Manifest
	assert.Equal(t, "fixture", m.Name)
	assert.Equal(t, "1.2.3", m.Version)
	require.Len(t, m.Kinds, 1)
	assert.Equal(t, pluginapi.KindPrompt, m.Kinds[0], "Manifest = %+v", m)
	require.Len(t, m.ConfigSchema, 1)
	assert.Equal(t, "greeting", m.ConfigSchema[0].Key)

	wantBuild := pluginapi.BuildInfo{GoVersion: "go-fixture", PluginAPIVersion: pluginapi.Version}
	assert.Equal(t, wantBuild, loaded.BuildInfo)
	assert.Equal(t, wantBuild, m.BuiltWith)

	a, b := loaded.Factory(), loaded.Factory()
	require.NotEqual(t, b, a)

	type serial interface{ Serial() int }
	sa, sb := a.(serial).Serial(), b.(serial).Serial()
	require.NotEqual(t, sb, sa)

	hook, ok := a.(pluginapi.PromptHook)
	require.True(t, ok)

	x := &pluginapi.Exchange{Prompt: &pluginapi.Prompt{Messages: []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, "block me")}}}
	d, err := hook.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionBlock, d.Action, "OnPrompt() = %+v, %v; want block", d, err)
}

func TestLoad_Fixture(t *testing.T) {
	so := fixtureSO(t, "fixture")
	dir := filepath.Dir(so)
	sum, err := FileSHA256(so)
	require.NoError(t, err)

	loaded, err := Load(config.PluginsConfig{
		SearchPaths: []string{dir},
		Load:        []config.PluginFileConfig{{File: filepath.Base(so), SHA256: sum}},
	})
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "fixture", loaded[0].Manifest.Name)

	_, err = Load(config.PluginsConfig{Load: []config.PluginFileConfig{{File: so, SHA256: strings.Repeat("0", 64)}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sha256 mismatch")
}

func TestOpen_MissingSymbol(t *testing.T) {
	so := fixtureSO(t, "nosymbol")
	_, err := Open(so)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not export GoModelPlugin")
}

func TestOpen_WrongSymbolType(t *testing.T) {
	so := fixtureSO(t, "badsymbol")
	_, err := Open(so)
	require.Error(t, err)
	require.Contains(t, err.Error(), "symbol GoModelPlugin has type *int")
}

func TestOpen_NotASharedObject(t *testing.T) {
	skipUnlessLoadable(t)
	path := filepath.Join(t.TempDir(), "garbage.so")
	err := os.WriteFile(path, []byte("garbage"), 0o644)
	require.NoError(t, err)

	_, err = Open(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), path)
}
