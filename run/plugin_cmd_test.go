package run

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/pluginload"
	"github.com/enterpilot/gomodel/pluginapi"
)

// raceEnabled is set by race_test.go; plugins built without -race cannot be
// loaded into a -race test binary.
var raceEnabled = false

func TestParseCLI_PluginSubcommand(t *testing.T) {
	opts, err := parseCLI("gomodel", []string{"plugin", "inspect", "x.so"}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI(plugin ...) error = %v", err)
	}
	if got := strings.Join(opts.PluginArgs, " "); got != "inspect x.so" {
		t.Fatalf("PluginArgs = %q, want %q", got, "inspect x.so")
	}
	opts, err = parseCLI("gomodel", []string{"plugin"}, io.Discard)
	if err != nil || opts.PluginArgs == nil || len(opts.PluginArgs) != 0 {
		t.Fatalf("parseCLI(plugin) = %+v, %v; want empty non-nil PluginArgs", opts, err)
	}
	opts, err = parseCLI("gomodel", []string{"--version"}, io.Discard)
	if err != nil || opts.PluginArgs != nil {
		t.Fatalf("parseCLI(--version).PluginArgs = %v, want nil", opts.PluginArgs)
	}
}

func TestRunPluginCommand_Usage(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "missing subcommand", args: nil, wantCode: 2, wantErr: "missing subcommand"},
		{name: "unknown subcommand", args: []string{"frobnicate"}, wantCode: 2, wantErr: `unknown subcommand "frobnicate"`},
		{name: "build without dir", args: []string{"build"}, wantCode: 2, wantErr: "missing plugin directory"},
		{name: "build extra args", args: []string{"build", "a", "b"}, wantCode: 2, wantErr: "unexpected arguments"},
		{name: "inspect without file", args: []string{"inspect"}, wantCode: 2, wantErr: "expected exactly one argument"},
		{name: "help", args: []string{"help"}, wantCode: 0},
		{name: "build help", args: []string{"build", "-h"}, wantCode: 0},
		{name: "build missing dir", args: []string{"build", filepath.Join(t.TempDir(), "nope")}, wantCode: 1, wantErr: "is not a directory"},
		{name: "inspect missing file", args: []string{"inspect", filepath.Join(t.TempDir(), "nope.so")}, wantCode: 1, wantErr: "nope.so"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runPluginCommand(context.Background(), "gomodel", tt.args, &stdout, &stderr)
			if got := ExitCode(err); got != tt.wantCode {
				t.Fatalf("ExitCode() = %d (err %v), want %d", got, err, tt.wantCode)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			// `plugin help` writes to stdout; `build -h` uses the flag
			// package's stderr convention.
			if tt.wantCode == 0 && !strings.Contains(stdout.String()+stderr.String(), "plugin build") {
				t.Fatalf("help output = %q / %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRun_DispatchesPluginSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Args:   []string{"plugin", "help"},
		Stdout: &stdout,
		Stderr: &stderr,
		Setup:  func(context.Context) error { t.Error("Setup must not run for plugin subcommands"); return nil },
	})
	if err != nil {
		t.Fatalf("Run(plugin help) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "plugin inspect") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestParsePluginBuildArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantDir string
		wantOut string
		wantErr bool
	}{
		{name: "dir only", args: []string{"./examples/plugins/keywordblock"}, wantDir: "./examples/plugins/keywordblock", wantOut: "keywordblock.so"},
		{name: "flag before dir", args: []string{"-o", "out/x.so", "./p"}, wantDir: "./p", wantOut: "out/x.so"},
		{name: "flag after dir", args: []string{"./p", "-o", "x.so"}, wantDir: "./p", wantOut: "x.so"},
		{name: "double dash flag", args: []string{"./p", "--o=x.so"}, wantDir: "./p", wantOut: "x.so"},
		{name: "unknown flag", args: []string{"./p", "--bogus"}, wantErr: true},
		{name: "no dir", args: []string{"-o", "x.so"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parsePluginBuildArgs("gomodel", tt.args, io.Discard)
			if tt.wantErr {
				if err == nil {
					t.Fatal("error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if opts.Dir != tt.wantDir || opts.Out != tt.wantOut {
				t.Fatalf("opts = %+v, want dir %q out %q", opts, tt.wantDir, tt.wantOut)
			}
		})
	}
}

func TestBuildInfoOverlay(t *testing.T) {
	src := buildInfoSource(pluginapi.BuildInfo{GoVersion: "go1.99.0", PluginAPIVersion: "9.9.9"})
	for _, want := range []string{"package main", `"github.com/enterpilot/gomodel/pluginapi"`, `var GoModelBuildInfo = pluginapi.BuildInfo{GoVersion: "go1.99.0", PluginAPIVersion: "9.9.9"}`, "DO NOT EDIT"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source lacks %q:\n%s", want, src)
		}
	}

	dir := t.TempDir()
	overlay, cleanup, err := writeBuildInfoOverlay(dir, pluginload.HostBuildInfo)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	virtual := filepath.Join(dir, buildInfoFile)
	if !strings.Contains(string(data), `"Replace"`) || !strings.Contains(string(data), virtual) {
		t.Fatalf("overlay = %s", data)
	}
	cleanup()
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s (err %v)", overlay, err)
	}
}

func TestDeclaresBuildInfo(t *testing.T) {
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{name: "absent", files: map[string]string{"main.go": "package main\n\nvar other = 1\n"}, want: false},
		{name: "declared", files: map[string]string{"main.go": "package main\n", "info.go": "package main\n\nvar GoModelBuildInfo = struct{}{}\n"}, want: true},
		{name: "grouped var", files: map[string]string{"main.go": "package main\n\nvar (\n\tx = 1\n\tGoModelBuildInfo = 2\n)\n"}, want: true},
		{name: "only in test file", files: map[string]string{"main.go": "package main\n", "main_test.go": "package main\n\nvar GoModelBuildInfo = 1\n"}, want: false},
		{name: "function not var", files: map[string]string{"main.go": "package main\n\nfunc GoModelBuildInfo() {}\n"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				write(dir, name, content)
			}
			got, err := declaresBuildInfo(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("declaresBuildInfo() = %t, want %t", got, tt.want)
			}
		})
	}
	dir := t.TempDir()
	write(dir, "broken.go", "package main\n\nvar = \n")
	if _, err := declaresBuildInfo(dir); err == nil {
		t.Fatal("declaresBuildInfo(broken) error = nil")
	}
}

func TestWriteManifest(t *testing.T) {
	var out bytes.Buffer
	writeManifest(&out, pluginload.Loaded{
		Path: "/p/x.so",
		Manifest: pluginapi.Manifest{
			Name: "x", Version: "1.0", Kinds: []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse},
			ConfigSchema: []pluginapi.Field{
				{Key: "keywords", Label: "Keywords", Input: pluginapi.InputTextarea, Required: true},
				{Key: "action", Input: pluginapi.InputSelect, Default: "block", Scope: pluginapi.ScopeRoute},
			},
		},
		BuildInfo:      pluginapi.BuildInfo{GoVersion: "go1.27.0", PluginAPIVersion: "0.1.0"},
		SingleInstance: true,
	})
	for _, want := range []string{"name  ", "x\n", "prompt, response", "go1.27.0, pluginapi 0.1.0", "one (GoModelPlugin is a variable)", "keywords", "textarea", "true", "action", "route", "block"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	writeManifest(&out, pluginload.Loaded{Manifest: pluginapi.Manifest{Name: "bare"}})
	if !strings.Contains(out.String(), "no GoModelBuildInfo") || !strings.Contains(out.String(), "config       -") {
		t.Errorf("bare output:\n%s", out.String())
	}
}

// TestPluginBuildAndInspect builds the loader fixture through the CLI and
// inspects the result. It needs a cgo-enabled Go toolchain on a platform
// that supports plugins, and a non-race test binary built with the same
// flags as the plugin.
func TestPluginBuildAndInspect(t *testing.T) {
	if !pluginload.Supported || raceEnabled || testing.Short() {
		t.Skip("plugin loading unavailable in this test binary")
	}
	if out, err := exec.Command("go", "env", "CGO_ENABLED").Output(); err != nil || strings.TrimSpace(string(out)) == "0" {
		t.Skip("CGO_ENABLED=0 or go unavailable")
	}
	fixture, err := filepath.Abs("../internal/pluginload/testdata/fixture")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "fixture.so")

	var stdout, stderr bytes.Buffer
	err = runPluginCommand(context.Background(), "gomodel", []string{"build", "-o", out, fixture}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("plugin build error = %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "host: "+runtime.Version()) || !strings.Contains(stdout.String(), "built "+out) {
		t.Fatalf("build stdout = %q", stdout.String())
	}

	stdout.Reset()
	if err := runPluginCommand(context.Background(), "gomodel", []string{"inspect", out}, &stdout, &stderr); err != nil {
		t.Fatalf("plugin inspect error = %v", err)
	}
	// The fixture declares its own GoModelBuildInfo, so the overlay must not
	// have replaced it.
	for _, want := range []string{"fixture", "1.2.3", "prompt", "go-fixture, pluginapi " + pluginapi.Version, "greeting"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("inspect output lacks %q:\n%s", want, stdout.String())
		}
	}

	// A package without the symbol gets it stamped through the overlay. The
	// copy must live inside the module to import pluginapi, so it goes under
	// the fixture's testdata tree (skipped by ./... patterns) rather than a
	// temp dir.
	src, err := os.ReadFile(filepath.Join(fixture, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.Replace(string(src), "var GoModelBuildInfo = pluginapi.BuildInfo{", "var unusedBuildInfo = pluginapi.BuildInfo{", 1)
	inModule := filepath.Join(fixture, "..", "stamped-"+filepath.Base(t.TempDir()))
	if err := os.MkdirAll(inModule, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(inModule) })
	if err := os.WriteFile(filepath.Join(inModule, "main.go"), []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}

	out2 := filepath.Join(t.TempDir(), "stamped.so")
	stdout.Reset()
	if err := runPluginCommand(context.Background(), "gomodel", []string{"build", "-o", out2, inModule}, &stdout, &stderr); err != nil {
		t.Fatalf("plugin build (stamped) error = %v\nstderr: %s", err, stderr.String())
	}
	loaded, err := pluginload.Open(out2)
	if err != nil {
		t.Fatalf("Open(stamped) error = %v", err)
	}
	if loaded.BuildInfo != pluginload.HostBuildInfo {
		t.Fatalf("stamped BuildInfo = %+v, want %+v", loaded.BuildInfo, pluginload.HostBuildInfo)
	}
}
