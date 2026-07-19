package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

func TestDefaultSQLitePathUsesLegacyWhenDataDirExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if got := DefaultSQLitePath(); got != LegacySQLitePath {
		t.Errorf("DefaultSQLitePath() = %q, want legacy %q when ./data exists", got, LegacySQLitePath)
	}
}

func TestDefaultSQLitePathIgnoresDataFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if got := DefaultSQLitePath(); got == LegacySQLitePath {
		t.Errorf("DefaultSQLitePath() returned the legacy path although ./data is a regular file")
	}
}

func TestDefaultSQLitePathUsesPlatformDirOtherwise(t *testing.T) {
	t.Chdir(t.TempDir())

	dataDir, err := platformdir.DataDir()
	if err != nil {
		t.Fatalf("platformdir.DataDir() error: %v", err)
	}
	want := filepath.Join(dataDir, "gomodel.db")
	if got := DefaultSQLitePath(); got != want {
		t.Errorf("DefaultSQLitePath() = %q, want %q without ./data", got, want)
	}
}
