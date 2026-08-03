package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

// The pid file has to land next to the database, or `gomodel --reload` looks
// for it somewhere the gateway never wrote it: a Docker image with /app/data
// keeps both project-local, a binary install keeps both in the per-user data
// directory.
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

func TestPIDFileEnvOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PID_FILE", "/var/run/gomodel/custom.pid")

	result, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := result.Config.Server.PIDFile; got != "/var/run/gomodel/custom.pid" {
		t.Errorf("Server.PIDFile = %q, want the PID_FILE value", got)
	}
}
