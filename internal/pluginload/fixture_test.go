package pluginload

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// raceEnabled is set by race_test.go when the test binary is built with
// -race; a plugin built without -race cannot be loaded into it.
var raceEnabled = false

var fixtures struct {
	sync.Mutex
	dir   string
	built map[string]string
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtures.dir != "" {
		_ = os.RemoveAll(fixtures.dir)
	}
	os.Exit(code)
}

// skipUnlessLoadable skips tests that need to open a real shared object.
func skipUnlessLoadable(t *testing.T) {
	t.Helper()
	if !Supported {
		t.Skip("plugin loading is not supported on this platform")
	}
	if raceEnabled {
		t.Skip("plugins built without -race cannot be loaded into a -race test binary")
	}
	if testing.Short() {
		t.Skip("building fixture plugins is skipped in -short mode")
	}
	out, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil {
		t.Skipf("go env: %v", err)
	}
	if strings.TrimSpace(string(out)) == "0" {
		t.Skip("CGO_ENABLED=0: plugins cannot be built or loaded")
	}
}

// fixtureSO builds testdata/<name> as a plugin once per test binary and
// returns the path of the .so. It is built with the same flags as the test
// binary (GOFLAGS applies to both), and deliberately without -trimpath: a
// -trimpath mismatch between host and plugin makes plugin.Open refuse the
// file.
func fixtureSO(t *testing.T, name string) string {
	t.Helper()
	skipUnlessLoadable(t)
	fixtures.Lock()
	defer fixtures.Unlock()
	if fixtures.dir == "" {
		dir, err := os.MkdirTemp("", "pluginload-fixtures-")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		fixtures.dir = dir
		fixtures.built = map[string]string{}
	}
	if path, ok := fixtures.built[name]; ok {
		return path
	}
	out := filepath.Join(fixtures.dir, name+".so")
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", out, "./testdata/"+name)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building fixture %s: %v\n%s", name, err, output)
	}
	fixtures.built[name] = out
	return out
}
