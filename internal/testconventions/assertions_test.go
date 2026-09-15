// Package testconventions holds repository-wide checks on how tests are
// written, so conventions documented in AGENTS.md do not erode.
package testconventions

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skippedDirs are trees that hold no Go tests of ours.
var skippedDirs = map[string]bool{
	".git":         true,
	".cache":       true,
	"node_modules": true,
	"third_party":  true,
	"vendor":       true,
	"web":          true,
}

// TestNoHandRolledAssertions keeps tests on testify. It fails on an if
// statement whose only job is to fail the test, such as
//
//	if got != want {
//		t.Fatalf("got %v, want %v", got, want)
//	}
//
// which require.Equal expresses directly (use assert inside goroutines).
// Unconditional failures, such as a select timeout or a switch default, stay
// allowed. Benchmarks are exempt: testify marks every call as a helper, which
// would add cost inside timed loops.
func TestNoHandRolledAssertions(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
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
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			stmt, ok := n.(*ast.IfStmt)
			if ok && stmt.Else == nil && onlyFailsTest(stmt.Body) {
				pos := fset.Position(stmt.Pos())
				rel, relErr := filepath.Rel(root, pos.Filename)
				if relErr != nil {
					rel = pos.Filename
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), pos.Line))
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "replace these hand-rolled assertions with testify require/assert:\n%s", strings.Join(offenders, "\n"))
}

// onlyFailsTest reports whether body is just a Fatal, Fatalf, Error, or
// Errorf call on a test (t or tb), optionally followed by a bare return.
func onlyFailsTest(body *ast.BlockStmt) bool {
	stmts := body.List
	if len(stmts) == 2 {
		if _, ok := stmts[1].(*ast.ReturnStmt); !ok {
			return false
		}
		stmts = stmts[:1]
	}
	if len(stmts) != 1 {
		return false
	}
	expr, ok := stmts[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if recv.Name != "t" && recv.Name != "tb" {
		return false
	}
	switch sel.Sel.Name {
	case "Fatal", "Fatalf", "Error", "Errorf":
		return true
	}
	return false
}
