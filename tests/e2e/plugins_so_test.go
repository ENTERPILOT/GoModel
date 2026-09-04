//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/pluginload"
)

// TestPlugins_SharedObject_E2E builds the keyword_block example with
// `gomodel plugin build`, loads it into a gateway, and checks that an
// instance blocks a prompt. Go plugins need cgo on linux, darwin or freebsd
// and a shared object built with the host's exact toolchain and flags; the
// test skips when that cannot hold for this test binary.
func TestPlugins_SharedObject_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping shared object build in -short mode")
	}
	if !pluginload.Supported {
		t.Skip("shared object plugins are not supported on this platform")
	}
	if reason := unsupportedHostBuild(); reason != "" {
		t.Skip(reason)
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	dir := t.TempDir()
	so := filepath.Join(dir, "keyword_block.so")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "run", "./cmd/gomodel", "plugin", "build", "-o", so, "./examples/plugins/keywordblock")
	build.Dir = root
	out, err := build.CombinedOutput()
	require.NoError(t, err, "gomodel plugin build failed:\n%s", string(out))

	loaded, err := pluginload.Load(config.PluginsConfig{
		SearchPaths: []string{dir},
		Load:        []config.PluginFileConfig{{File: "keyword_block.so"}},
	})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "different toolchain") || strings.Contains(msg, "different version of package") || strings.Contains(msg, "not supported") {
			t.Skipf("shared object refused by this test binary (built by go test, not gomodel): %v", err)
		}
		require.NoError(t, err)
	}
	require.Len(t, loaded, 1)
	assert.Equal(t, "keyword_block", loaded[0].Manifest.Name)
	// The loader reports the symlink-resolved path (macOS temp dirs live
	// under /private/var).
	so, err = filepath.EvalSymlinks(so)
	require.NoError(t, err)

	fx := setupPluginServer(t, loaded...)
	t.Cleanup(func() { fx.reset(t) })

	var listed []struct {
		Name   string   `json:"name"`
		Kinds  []string `json:"kinds"`
		Source string   `json:"source"`
		Health string   `json:"health"`
	}
	fx.adminJSON(t, http.MethodGet, adminPluginsPath, nil, http.StatusOK, &listed)
	var found bool
	for _, p := range listed {
		if p.Name != "keyword_block" {
			continue
		}
		found = true
		assert.Equal(t, so, p.Source)
		assert.Equal(t, "ok", p.Health)
		assert.ElementsMatch(t, []string{"prompt", "response"}, p.Kinds)
	}
	require.True(t, found, "GET /admin/plugins should list the loaded shared object")

	fx.mustPutGuardrail(t, guardrailDef("keywords", "keyword_block", map[string]any{"keywords": "forbidden\nbanned"}, nil))
	fx.activate(t, workflowStep{Ref: "keywords", Phase: "prompt", Step: 1})

	mockServer.ResetRequests()
	envelope := readError(t, fx.chat(t, "this is Forbidden territory", false), http.StatusBadRequest)
	assert.Equal(t, "keyword_block", envelope.Error.Code)
	assert.Equal(t, "This request was stopped by the keyword policy.", envelope.Error.Message)
	assert.Empty(t, mockServer.Requests(), "blocked prompt must not reach the provider")

	_, text := readChat(t, fx.chat(t, "this is fine", false))
	assert.Equal(t, "Mock response to: this is fine", text)
}

// unsupportedHostBuild returns a skip reason when this test binary cannot
// load a shared object built by `gomodel plugin build`: cgo off, or a -race
// build (the plugin is built from the plain gomodel binary's flags).
func unsupportedHostBuild() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "build info unavailable; cannot verify cgo"
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "CGO_ENABLED":
			if s.Value != "1" {
				return "cgo is disabled; Go plugins need CGO_ENABLED=1"
			}
		case "-race":
			if s.Value == "true" {
				return "race-enabled test binary cannot load a non-race shared object"
			}
		}
	}
	return ""
}
