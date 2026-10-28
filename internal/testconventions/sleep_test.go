package testconventions

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sleepBudget is the number of time.Sleep calls each test file may still
// contain outside a testing/synctest bubble, where a sleep advances a fake
// clock and costs nothing. It is a ratchet: a file not listed here gets zero,
// a file may never exceed its entry, and an entry higher than the file's
// actual count fails as stale so the number only ever goes down. Lower the
// entry when you remove a sleep; add one only for a sleep that simulates
// latency inside a fake upstream or plugin, never to wait for the code under
// test (use testing/synctest, require.Eventually, or a channel for that).
var sleepBudget = map[string]int{
	"internal/admin/handler_live_test.go":             1,
	"internal/encryption/box_test.go":                 2,
	"internal/llmclient/client_test.go":               1,
	"internal/llmclient/slow_call_test.go":            1,
	"internal/llmclient/stream_idle_test.go":          1,
	"internal/plugins/abandon_test.go":                5,
	"internal/server/audio_service_test.go":           1,
	"internal/server/handlers_test.go":                1,
	"internal/server/realtime_translation_test.go":    1,
	"internal/server/realtime_webrtc_service_test.go": 1,
	"internal/server/stall_deadline_test.go":          1,
	"internal/server/version_handler_test.go":         1,
	"internal/streaming/slowdown_stream_test.go":      1,
	"internal/virtualmodels/plugin_strategy_test.go":  1,
	"tests/e2e/auditlog_test.go":                      6,
	"tests/e2e/main_test.go":                          1,
	"tests/e2e/opentelemetry_test.go":                 2,
	"tests/e2e/otlp_collector_test.go":                1,
	"tests/e2e/plugins_harness_test.go":               1,
	"tests/integration/setup_test.go":                 1,
	"tests/stress/stress_test.go":                     5,
}

// TestSleepBudget keeps time.Sleep out of tests. Sleeping to wait for a
// goroutine or timer is the one class of flake this suite does not have, and
// the budget keeps it that way.
func TestSleepBudget(t *testing.T) {
	root := filepath.Join("..", "..")
	counts := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if n := countSleeps(file); n > 0 {
			counts[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	require.NoError(t, err)

	var over, stale []string
	for file, n := range counts {
		if n > sleepBudget[file] {
			over = append(over, fmt.Sprintf("%s: %d time.Sleep calls, budget %d", file, n, sleepBudget[file]))
		}
	}
	for file, n := range sleepBudget {
		if counts[file] < n {
			stale = append(stale, fmt.Sprintf("%s: budget %d, now %d", file, n, counts[file]))
		}
	}
	slices.Sort(over)
	slices.Sort(stale)
	assert.Empty(t, over, "time.Sleep in tests over budget; wait with testing/synctest, require.Eventually, or a channel instead:\n%s", strings.Join(over, "\n"))
	assert.Empty(t, stale, "sleepBudget entries above the actual count; lower them so the ratchet holds:\n%s", strings.Join(stale, "\n"))
}

// countSleeps returns how many time.Sleep calls the file contains outside
// synctest.Test bubbles, whatever names it imports the two packages by.
func countSleeps(file *ast.File) int {
	timePkg := importName(file, "time")
	if timePkg == "" {
		return 0
	}
	synctestPkg := importName(file, "testing/synctest")
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch {
		case synctestPkg != "" && isCall(call, synctestPkg, "Test"):
			return false
		case isCall(call, timePkg, "Sleep"):
			n++
		}
		return true
	})
	return n
}

// importName returns the name file refers to importPath by, or "" when the
// file does not import it.
func importName(file *ast.File, importPath string) string {
	for _, spec := range file.Imports {
		if spec.Path.Value != strconv.Quote(importPath) {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path.Base(importPath)
	}
	return ""
}

func isCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

func TestCountSleeps(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{
			name: "plain imports",
			src: `package p
import (
	"testing/synctest"
	"time"
)
func f(d time.Duration) {
	time.Sleep(d)
	time.Sleep(10 * time.Millisecond)
	other.Sleep(d)
	_ = time.Now()
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(d)
	})
}`,
			want: 2,
		},
		{
			name: "aliased imports",
			src: `package p
import (
	st "testing/synctest"
	clock "time"
)
func f(d clock.Duration) {
	clock.Sleep(d)
	time.Sleep(d)
	st.Test(t, func(t *testing.T) { clock.Sleep(d) })
}`,
			want: 1,
		},
		{
			name: "time not imported",
			src: `package p
func f() { time.Sleep(1) }`,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "x_test.go", tt.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			assert.Equal(t, tt.want, countSleeps(file))
		})
	}
}
