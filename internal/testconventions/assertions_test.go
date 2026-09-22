// Package testconventions holds repository-wide checks on how tests are
// written, so conventions documented in AGENTS.md do not erode.
package testconventions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skippedDirs are trees that hold no Go tests of ours.
var skippedDirs = map[string]bool{
	".git":         true,
	".claude":      true, // local agent worktrees, gitignored
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
		for _, pos := range handRolledAssertions(fset, file) {
			rel, relErr := filepath.Rel(root, pos.Filename)
			if relErr != nil {
				rel = pos.Filename
			}
			offenders = append(offenders, filepath.ToSlash(rel)+":"+strconv.Itoa(pos.Line))
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "replace these hand-rolled assertions with testify require/assert:\n%s", strings.Join(offenders, "\n"))
}

func TestHandRolledAssertionsDetection(t *testing.T) {
	tests := []struct {
		name   string
		header string
		src    string
		want   int
	}{
		{name: "test failing in an if", src: `func TestX(t *testing.T) { if a != b { t.Fatalf("x") } }`, want: 1},
		{name: "any receiver name", src: `func TestX(x *testing.T) { if a != b { x.Errorf("x") } }`, want: 1},
		{name: "helper taking testing.TB", src: `func check(tb testing.TB) { if a != b { tb.Fatal("x") } }`, want: 1},
		{name: "closure inherits the test", src: `func TestX(t *testing.T) { t.Run("s", func(t *testing.T) { if a { t.Error("x"); return } }) }`, want: 1},
		{name: "benchmark named t", src: `func BenchmarkX(t *testing.B) { if a != b { t.Fatal("x") } }`, want: 0},
		{name: "benchmark", src: `func BenchmarkX(b *testing.B) { if err != nil { b.Fatal(err) } }`, want: 0},
		{name: "return with a value", src: `func TestX(t *testing.T) { f := func() error { if a { t.Error("x"); return err }; return nil }; _ = f }`, want: 0},
		{name: "other work in the body", src: `func TestX(t *testing.T) { if a { cleanup(); t.Fatal("x") } }`, want: 0},
		{name: "unconditional failure", src: `func TestX(t *testing.T) { select { case <-done: default: t.Fatal("x") } }`, want: 0},
		{name: "if with else", src: `func TestX(t *testing.T) { if a { t.Fatal("x") } else { ok() } }`, want: 0},
		{name: "local shadows the test in an if initializer", src: `func TestX(t *testing.T) { if t := other(); t.Bad() { t.Error("x") } }`, want: 0},
		{name: "local shadows the test in a nested block", src: `func TestX(t *testing.T) { { t := fake{}; if a { t.Fatal("x") } } }`, want: 0},
		{name: "aliased testing import", header: `import stdtesting "testing"`, src: `func TestX(t *stdtesting.T) { if a != b { t.Fatal("x") } }`, want: 1},
		{name: "dot testing import", header: `import . "testing"`, src: `func TestX(t *T) { if a != b { t.Fatal("x") } }`, want: 1},
		{name: "closure shadowing leaves the outer test checked", src: `func TestX(t *testing.T) { f := func() { t := fake{}; if a { t.Fatal("x") } }; _ = f; if b { t.Fatal("y") } }`, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			header := tt.header
			if header == "" {
				header = `import "testing"`
			}
			file, err := parser.ParseFile(fset, "x_test.go", "package x\n"+header+"\n"+tt.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			assert.Len(t, handRolledAssertions(fset, file), tt.want)
		})
	}
}

// handRolledAssertions returns the position of every if statement in file
// that only fails a test or test helper.
func handRolledAssertions(fset *token.FileSet, file *ast.File) []token.Position {
	var found []token.Position
	ast.Walk(finder{fset: fset, testingPkg: testingImportName(file), found: &found}, file)
	return found
}

// testingImportName returns the name file refers to the testing package by:
// its alias, "." for a dot import, or "" when file does not import it.
func testingImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		if spec.Path.Value != `"testing"` {
			continue
		}
		if spec.Name == nil {
			return "testing"
		}
		return spec.Name.Name
	}
	return ""
}

// finder walks a file while tracking which identifiers in scope are
// *testing.T or testing.TB parameters, so exemptions follow the declared
// type rather than the variable name.
type finder struct {
	fset       *token.FileSet
	testingPkg string
	params     map[string]string
	found      *[]token.Position
}

func (f finder) Visit(n ast.Node) ast.Visitor {
	switch n := n.(type) {
	case *ast.FuncDecl:
		return f.withParams(n.Type, n.Body)
	case *ast.FuncLit:
		return f.withParams(n.Type, n.Body)
	case *ast.IfStmt:
		if n.Else == nil && f.onlyFailsTest(n.Body) {
			*f.found = append(*f.found, f.fset.Position(n.Pos()))
		}
	}
	return f
}

// withParams returns a finder that also knows fn's testing parameters. A
// parameter of any other type, or a local variable declared in body, shadows
// a testing parameter of the same name; the check then skips that name in
// this function rather than guess which declaration a call refers to.
func (f finder) withParams(fn *ast.FuncType, body *ast.BlockStmt) finder {
	params := maps.Clone(f.params)
	if params == nil {
		params = map[string]string{}
	}
	for _, field := range fn.Params.List {
		kind := f.testingType(field.Type)
		for _, name := range field.Names {
			if kind == "" {
				delete(params, name.Name)
			} else {
				params[name.Name] = kind
			}
		}
	}
	for name := range localNames(body) {
		delete(params, name)
	}
	f.params = params
	return f
}

// localNames returns the variables body declares with :=, var, or range.
// Nested function literals are skipped; they get their own scope when the
// walk reaches them.
func localNames(body *ast.BlockStmt) map[string]bool {
	names := map[string]bool{}
	if body == nil {
		return names
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						names[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				names[id.Name] = true
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				for _, expr := range []ast.Expr{n.Key, n.Value} {
					if id, ok := expr.(*ast.Ident); ok {
						names[id.Name] = true
					}
				}
			}
		}
		return true
	})
	return names
}

// onlyFailsTest reports whether body is just a Fatal, Fatalf, Error, or
// Errorf call on a *testing.T or testing.TB, optionally followed by a bare
// return.
func (f finder) onlyFailsTest(body *ast.BlockStmt) bool {
	stmts := body.List
	if len(stmts) == 2 {
		ret, ok := stmts[1].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 0 {
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
	if kind := f.params[recv.Name]; kind != "T" && kind != "TB" {
		return false
	}
	switch sel.Sel.Name {
	case "Fatal", "Fatalf", "Error", "Errorf":
		return true
	}
	return false
}

// testingType returns T, B, TB, or F for a parameter typed with the file's
// testing package, however it is imported, or "".
func (f finder) testingType(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && f.testingPkg != "" && pkg.Name == f.testingPkg {
			return x.Sel.Name
		}
	case *ast.Ident:
		if f.testingPkg == "." {
			return x.Name
		}
	}
	return ""
}
