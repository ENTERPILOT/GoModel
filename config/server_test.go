package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

// The pid file follows the database instead of scattering GoModel's state
// across the filesystem: a Docker image with /app/data keeps both
// project-local, and a binary install started from an arbitrary working
// directory keeps both in the per-user data directory.
func TestDefaultPIDFilePath(t *testing.T) {
	platformDataDir, err := platformdir.DataDir()
	if err != nil {
		t.Fatalf("platformdir.DataDir() error: %v", err)
	}

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "data directory exists keeps the project-local path",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "data"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: LegacyPIDFilePath,
		},
		{
			name:  "no data directory uses the platform path",
			setup: func(t *testing.T, dir string) {},
			want:  filepath.Join(platformDataDir, "gomodel.pid"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			t.Chdir(dir)

			if got := DefaultPIDFilePath(); got != tt.want {
				t.Errorf("DefaultPIDFilePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPIDFilePathResolution(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		configYAML string
		want       string
	}{
		{
			name: "env var wins",
			env:  "/var/run/gomodel/custom.pid",
			want: "/var/run/gomodel/custom.pid",
		},
		{
			// Empty env vars are "unset" everywhere in this config, so PID_FILE=
			// keeps the default rather than disabling the pid file. Asserted so
			// the documented way to disable it stays the config file.
			name: "empty env var keeps the default",
			env:  "",
			want: DefaultPIDFilePath(),
		},
		{
			name:       "empty config value writes no pid file",
			configYAML: "server:\n  pid_file: \"\"\n",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("PID_FILE", tt.env)
			if tt.configYAML != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tt.configYAML), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			result, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got := result.Config.Server.PIDFile; got != tt.want {
				t.Errorf("Server.PIDFile = %q, want %q", got, tt.want)
			}
		})
	}
}
