package pluginload

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	root := t.TempDir()
	search := filepath.Join(root, "plugins")
	other := filepath.Join(root, "other")
	writeFile(t, filepath.Join(search, "a.so"), "a")
	writeFile(t, filepath.Join(search, "sub", "b.so"), "b")
	writeFile(t, filepath.Join(other, "c.so"), "c")
	writeFile(t, filepath.Join(root, "outside.so"), "o")
	if err := os.Symlink(filepath.Join(root, "outside.so"), filepath.Join(search, "link.so")); err != nil {
		t.Skipf("symlink: %v", err)
	}

	tests := []struct {
		name        string
		file        string
		searchPaths []string
		want        string
		wantErr     string
	}{
		{name: "relative in first search path", file: "a.so", searchPaths: []string{search, other}, want: filepath.Join(search, "a.so")},
		{name: "relative in later search path", file: "c.so", searchPaths: []string{search, other}, want: filepath.Join(other, "c.so")},
		{name: "relative subdirectory", file: "sub/b.so", searchPaths: []string{search}, want: filepath.Join(search, "sub", "b.so")},
		{name: "absolute path", file: filepath.Join(other, "c.so"), searchPaths: nil, want: filepath.Join(other, "c.so")},
		{name: "empty file", file: "  ", searchPaths: []string{search}, wantErr: "plugin file is empty"},
		{name: "relative without search paths", file: "a.so", searchPaths: nil, wantErr: "search_paths is empty"},
		{name: "relative not found", file: "missing.so", searchPaths: []string{search}, wantErr: `"missing.so" not found`},
		{name: "absolute not found", file: filepath.Join(root, "missing.so"), wantErr: "no such file"},
		{name: "escapes with dotdot", file: "../outside.so", searchPaths: []string{search}, wantErr: "escapes search path"},
		{name: "escapes via symlink", file: "link.so", searchPaths: []string{search}, wantErr: "outside search path"},
		{name: "blank search path entries are skipped", file: "a.so", searchPaths: []string{"", search}, want: filepath.Join(search, "a.so")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.file, tt.searchPaths)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			// Relative files are returned with symlinks resolved; absolute
			// files as given.
			real, _ := filepath.EvalSymlinks(tt.want)
			if got != tt.want && got != real {
				t.Fatalf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifySHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.so")
	writeFile(t, path, "plugin bytes")
	sum := sha256.Sum256([]byte("plugin bytes"))
	good := hex.EncodeToString(sum[:])

	tests := []struct {
		name    string
		digest  string
		wantErr string
	}{
		{name: "empty skips", digest: ""},
		{name: "match", digest: good},
		{name: "match uppercase with prefix", digest: "SHA256:" + strings.ToUpper(good)},
		{name: "mismatch", digest: strings.Repeat("0", 64), wantErr: "sha256 mismatch"},
		{name: "wrong length", digest: "abc", wantErr: "not a 64-character hex digest"},
		{name: "not hex", digest: strings.Repeat("z", 64), wantErr: "is not hex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySHA256(path, tt.digest)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("VerifySHA256() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("VerifySHA256() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}

	if _, err := FileSHA256(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("FileSHA256(missing) error = nil")
	}
}

func TestLoad_NothingConfigured(t *testing.T) {
	for _, cfg := range []config.PluginsConfig{
		{},
		{SearchPaths: []string{"/nonexistent/dir"}},
	} {
		got, err := Load(cfg)
		if err != nil || got != nil {
			t.Fatalf("Load(%+v) = %v, %v; want nil, nil", cfg, got, err)
		}
	}
}

func TestLoad_FailsBeforeOpening(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.so"), "not a shared object")

	tests := []struct {
		name    string
		cfg     config.PluginsConfig
		wantErr string
	}{
		{
			name:    "unknown file",
			cfg:     config.PluginsConfig{SearchPaths: []string{dir}, Load: []config.PluginFileConfig{{File: "nope.so"}}},
			wantErr: `plugins.load[0]: plugin file "nope.so" not found`,
		},
		{
			name:    "escaping path",
			cfg:     config.PluginsConfig{SearchPaths: []string{dir}, Load: []config.PluginFileConfig{{File: "../x.so"}}},
			wantErr: "escapes search path",
		},
		{
			name:    "sha mismatch",
			cfg:     config.PluginsConfig{SearchPaths: []string{dir}, Load: []config.PluginFileConfig{{File: "x.so", SHA256: strings.Repeat("a", 64)}}},
			wantErr: "sha256 mismatch",
		},
		{
			name:    "second entry reported by index",
			cfg:     config.PluginsConfig{Load: []config.PluginFileConfig{{File: filepath.Join(dir, "x.so"), SHA256: strings.Repeat("b", 64)}, {File: "x.so"}}},
			wantErr: "plugins.load[0]: plugin file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildFlags(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "-trimpath", Value: "true"},
		{Key: "-race", Value: "true"},
		{Key: "-tags", Value: "swagger,e2e"},
		{Key: "-gcflags", Value: "all=-N -l"},
		{Key: "CGO_ENABLED", Value: "1"},
	}
	f := flagsFromSettings(settings)
	want := []string{"-trimpath", "-race", "-tags=swagger,e2e", "-gcflags=all=-N -l"}
	if got := f.Args(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Args() = %v, want %v", got, want)
	}
	if s := (BuildFlags{}).String(); s != "(none)" {
		t.Fatalf("empty String() = %q", s)
	}
}

func TestModuleVersion(t *testing.T) {
	main := &debug.BuildInfo{Main: debug.Module{Path: hostModule, Version: "v1.2.3"}}
	if got := moduleVersion(main); got != "v1.2.3" {
		t.Fatalf("main module version = %q", got)
	}
	dep := &debug.BuildInfo{
		Main: debug.Module{Path: "example.com/custom"},
		Deps: []*debug.Module{{Path: hostModule, Version: "v1.0.0", Replace: &debug.Module{Path: "../gomodel", Version: "v1.0.1"}}},
	}
	if got := moduleVersion(dep); got != "v1.0.1" {
		t.Fatalf("replaced dep version = %q", got)
	}
	if got := moduleVersion(&debug.BuildInfo{}); got != "" {
		t.Fatalf("absent module version = %q", got)
	}
}

func TestDescribeOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.so")
	writeFile(t, path, "not a binary")

	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "different package version",
			err:  errors.New(`plugin.Open("p"): plugin was built with a different version of package internal/goarch`),
			want: []string{path, "different toolchain, flags, or pluginapi sources", "build info is unreadable", "this binary was built with " + HostBuildInfo.GoVersion, "gomodel plugin build"},
		},
		{
			name: "not implemented",
			err:  errors.New("plugin: not implemented"),
			want: []string{path, "CGO_ENABLED=1", "gomodel:<version>-plugins"},
		},
		{
			name: "other",
			err:  errors.New("dlopen failed"),
			want: []string{path, "dlopen failed", HostBuildInfo.GoVersion},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeOpenError(path, tt.err)
			if !errors.Is(got, tt.err) {
				t.Fatalf("describeOpenError() does not wrap the cause: %v", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got.Error(), w) {
					t.Errorf("describeOpenError() = %q, want containing %q", got, w)
				}
			}
		})
	}
}

func TestFactoryFromSymbol(t *testing.T) {
	if _, _, err := factoryFromSymbol(new(int)); err == nil || !strings.Contains(err.Error(), "*int") {
		t.Fatalf("factoryFromSymbol(*int) error = %v", err)
	}
	if _, err := buildInfoFromSymbol("x"); err == nil {
		t.Fatal("buildInfoFromSymbol(string) error = nil")
	}
}
