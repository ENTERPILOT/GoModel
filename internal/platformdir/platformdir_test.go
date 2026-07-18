package platformdir

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDataDir(t *testing.T) {
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("DataDir() = %q, want an absolute path", dir)
	}
	if filepath.Base(dir) != app {
		t.Errorf("DataDir() = %q, want a %q leaf directory", dir, app)
	}
}

func TestDataDirHonorsXDGDataHome(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG_DATA_HOME only applies to the default (Linux/Unix) branch")
	}
	t.Setenv("XDG_DATA_HOME", "/custom/data")

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	if want := filepath.Join("/custom/data", app); dir != want {
		t.Errorf("DataDir() = %q, want %q", dir, want)
	}
}

func TestCacheDir(t *testing.T) {
	dir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("CacheDir() = %q, want an absolute path", dir)
	}
	want := app
	if runtime.GOOS == "windows" {
		want = "cache"
	}
	if filepath.Base(dir) != want {
		t.Errorf("CacheDir() = %q, want a %q leaf directory", dir, want)
	}
}

func TestDataAndCacheDirsDiffer(t *testing.T) {
	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error: %v", err)
	}
	if dataDir == cacheDir {
		t.Errorf("DataDir() and CacheDir() are both %q; they must differ", dataDir)
	}
}
