package testconventions

import (
	"errors"
	"go/ast"
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
		if !ok || len(call.Args) != 1 || !isSelectorCall(call, "Add") {
			return true
		}
		arg, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok || arg.Sel.Name != "Registration" {
			return true
		}
		pkg, ok := arg.X.(*ast.Ident)
		if !ok {
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

// packageCallsChatContract reports whether any _test.go file in dir calls
// providertest.AssertChatCompatible.
func packageCallsChatContract(t *testing.T, dir string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	require.NoError(t, err)
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		if callsChatContract(parsed) {
			return true
		}
	}
	return false
}

// callsChatContract reports whether file calls AssertChatCompatible through
// its providertest import, whatever name it imports the package by.
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
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isSelectorCall(call, "AssertChatCompatible") {
			if recv, ok := call.Fun.(*ast.SelectorExpr).X.(*ast.Ident); ok && recv.Name == pkg {
				found = true
			}
		}
		return !found
	})
	return found
}

// isSelectorCall reports whether call is of the form <expr>.<name>(...).
func isSelectorCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}

// hasFixtures reports whether dir exists and holds at least one file.
func hasFixtures(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	require.NoError(t, err)
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool { return !e.IsDir() })
}
