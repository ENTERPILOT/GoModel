package testconventions

import (
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	registrationFile = "run/providers.go"
	providersPkgPath = "github.com/enterpilot/gomodel/internal/providers/"
	providertestPath = providersPkgPath + "providertest"
	fixturesDir      = "tests/contract/testdata"
)

// knownUncovered lists registered providers with neither a shared contract
// test nor recorded contract fixtures. It is a ratchet: the test fails when a
// provider is uncovered and missing here (a new provider shipped without a
// providertest.AssertChatCompatible call or tests/contract/testdata/<name>/
// fixtures), and when a listed provider has become covered (remove the stale
// entry so the list only shrinks). Keep it sorted.
var knownUncovered = []string{
	"audiocpp",
	"bedrock",
	"chatgpt",
	"cohere",
	"elevenlabs",
	"jev",
	"vertex",
}

// TestProviderContractCoverage checks that every provider registered in
// run/providers.go is covered by the shared OpenAI-compatible contract
// (AGENTS.md: such a provider gets one TestChatCompatibleContract) or by
// recorded replay fixtures under tests/contract/testdata, except for the
// providers in knownUncovered.
func TestProviderContractCoverage(t *testing.T) {
	root := filepath.Join("..", "..")
	providers := registeredProviders(t, filepath.Join(root, filepath.FromSlash(registrationFile)))
	require.NotEmpty(t, providers, "no factory.Add(<pkg>.Registration) calls found in %s", registrationFile)
	require.True(t, slices.IsSorted(knownUncovered), "knownUncovered must stay sorted")

	var uncovered []string
	for _, name := range providers {
		pkgDir := filepath.Join(root, "internal", "providers", name)
		if packageCallsChatContract(t, pkgDir) || hasFixtures(t, filepath.Join(root, filepath.FromSlash(fixturesDir), name)) {
			continue
		}
		uncovered = append(uncovered, name)
	}

	var missing, stale []string
	for _, name := range uncovered {
		if !slices.Contains(knownUncovered, name) {
			missing = append(missing, name)
		}
	}
	for _, name := range knownUncovered {
		if !slices.Contains(uncovered, name) {
			stale = append(stale, name)
		}
	}
	assert.Empty(t, missing, "providers without a providertest.AssertChatCompatible test or %s/<name>/ fixtures; "+
		"add one, or add the provider to knownUncovered with a reason:\n%s", fixturesDir, strings.Join(missing, "\n"))
	assert.Empty(t, stale, "providers in knownUncovered that are now covered; remove them from the list:\n%s", strings.Join(stale, "\n"))
}

func TestCallsChatContractDetection(t *testing.T) {
	const header = "package x\nimport pt \"" + providertestPath + "\"\n"
	parse := func(src string) *ast.File {
		file, err := parser.ParseFile(token.NewFileSet(), "x_test.go", header+src, parser.SkipObjectResolution)
		require.NoError(t, err)
		return file
	}
	assert.True(t, callsChatContract(parse(`func TestX(t *testing.T) { pt.AssertChatCompatible(t, pt.ChatCompatible{}) }`)), "aliased import")
	assert.False(t, callsChatContract(parse(`func TestX(t *testing.T) { providertest.AssertChatCompatible(t, nil) }`)), "unimported name")
	assert.False(t, callsChatContract(parse(`func TestX(t *testing.T) { pt.AssertSomethingElse(t) }`)), "other helper")
	assert.False(t, callsChatContract(parse(`func runContract(t *testing.T) { pt.AssertChatCompatible(t, pt.ChatCompatible{}) }`)), "helper no test calls")
	assert.False(t, callsChatContract(parse(`func (s suite) TestX(t *testing.T) { pt.AssertChatCompatible(t, pt.ChatCompatible{}) }`)), "method, not a top-level test")
}

// registeredProviders returns the package directory name of every provider
// that file registers with factory.Add(<pkg>.Registration), sorted.
func registeredProviders(t *testing.T, file string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	imports := map[string]string{}
	for _, spec := range parsed.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	var found []string
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSelectorCall(call, "Add") || !isIdent(call.Fun.(*ast.SelectorExpr).X, "factory") {
			return true
		}
		// A registration the parser cannot attribute to a package is one
		// the ratchet cannot check, so it is an error rather than a skip.
		var pkg *ast.Ident
		if len(call.Args) == 1 {
			if arg, ok := call.Args[0].(*ast.SelectorExpr); ok && arg.Sel.Name == "Registration" {
				pkg, _ = arg.X.(*ast.Ident)
			}
		}
		if pkg == nil {
			t.Errorf("%s: factory.Add must take <pkg>.Registration directly so provider coverage can be checked", registrationFile)
			return true
		}
		importPath, ok := imports[pkg.Name]
		require.True(t, ok, "%s: %s.Registration refers to an unknown import", registrationFile, pkg.Name)
		require.True(t, strings.HasPrefix(importPath, providersPkgPath), "%s: %s is registered from outside internal/providers", registrationFile, importPath)
		found = append(found, strings.TrimPrefix(importPath, providersPkgPath))
		return true
	})
	slices.Sort(found)
	return slices.Compact(found)
}

// packageCallsChatContract reports whether a _test.go file in dir that the
// default build (what `make test` and CI run) includes calls
// providertest.AssertChatCompatible from a test function.
func packageCallsChatContract(t *testing.T, dir string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	require.NoError(t, err)
	for _, file := range files {
		included, err := build.Default.MatchFile(dir, filepath.Base(file))
		require.NoError(t, err)
		if !included {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		if callsChatContract(parsed) {
			return true
		}
	}
	return false
}

// callsChatContract reports whether a top-level Test function in file calls
// AssertChatCompatible through its providertest import, whatever name it
// imports the package by. A call in a helper only counts through the test
// that makes it, which is the convention: one TestChatCompatibleContract.
func callsChatContract(file *ast.File) bool {
	pkg := ""
	for _, spec := range file.Imports {
		if spec.Path.Value != strconv.Quote(providertestPath) {
			continue
		}
		pkg = path.Base(providertestPath)
		if spec.Name != nil {
			pkg = spec.Name.Name
		}
	}
	if pkg == "" {
		return false
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isSelectorCall(call, "AssertChatCompatible") && isIdent(call.Fun.(*ast.SelectorExpr).X, pkg) {
				found = true
			}
			return !found
		})
	}
	return found
}

// isIdent reports whether expr is the identifier name.
func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

// isSelectorCall reports whether call is of the form <expr>.<name>(...).
func isSelectorCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}

// hasFixtures reports whether dir exists and holds at least one recorded
// payload: a JSON body or an SSE transcript, the two shapes the replay suite
// reads. A README alone does not count.
func hasFixtures(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	require.NoError(t, err)
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool {
		ext := path.Ext(e.Name())
		return !e.IsDir() && (ext == ".json" || ext == ".txt")
	})
}
