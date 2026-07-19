package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

func TestDefaultSQLitePath(t *testing.T) {
	platformDataDir, err := platformdir.DataDir()
	if err != nil {
		t.Fatalf("platformdir.DataDir() error: %v", err)
	}
	platformPath := filepath.Join(platformDataDir, "gomodel.db")

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "data directory exists keeps legacy path",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "data"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: LegacySQLitePath,
		},
		{
			name: "regular file named data falls through to platform path",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "data"), []byte("not a directory"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: platformPath,
		},
		{
			name:  "no data entry uses platform path",
			setup: func(t *testing.T, dir string) {},
			want:  platformPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			t.Chdir(dir)

			if got := DefaultSQLitePath(); got != tt.want {
				t.Errorf("DefaultSQLitePath() = %q, want %q", got, tt.want)
			}
		})
	}
}
