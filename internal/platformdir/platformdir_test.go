package platformdir

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataDir(t *testing.T) {
	dir, err := DataDir()
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(dir), "DataDir() = %q, want an absolute path", dir)
	assert.Equal(t, app, filepath.Base(dir), "DataDir() = %q, want a %q leaf directory", dir, app)
}

func TestDataDirHonorsXDGDataHome(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG_DATA_HOME only applies to the default (Linux/Unix) branch")
	}
	t.Setenv("XDG_DATA_HOME", "/custom/data")

	dir, err := DataDir()
	require.NoError(t, err)
	want := filepath.Join("/custom/data", app)
	assert.Equal(t, want, dir)
}

func TestCacheDir(t *testing.T) {
	dir, err := CacheDir()
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(dir), "CacheDir() = %q, want an absolute path", dir)

	want := app
	if runtime.GOOS == "windows" {
		want = "cache"
	}
	assert.Equal(t, want, filepath.Base(dir))
}

func TestDataAndCacheDirsDiffer(t *testing.T) {
	dataDir, err := DataDir()
	require.NoError(t, err)

	cacheDir, err := CacheDir()
	require.NoError(t, err)
	assert.NotEqual(t, cacheDir, dataDir)
}
