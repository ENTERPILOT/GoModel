package run

import (
	"context"
	"debug/buildinfo"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/enterpilot/gomodel/internal/pluginload"
	"github.com/enterpilot/gomodel/pluginapi"
)

// pluginCommand is the CLI word that selects the plugin tooling subcommands.
const pluginCommand = "plugin"

// buildInfoFile is the generated file that stamps GoModelBuildInfo into a
// plugin's main package during `gomodel plugin build`.
const buildInfoFile = "zz_gomodel_buildinfo.go"

func pluginUsage(productName string) string {
	return fmt.Sprintf(`Usage:
  %[1]s plugin build [-o out.so] <dir>   Build a plugin shared object from a package main directory
  %[1]s plugin inspect <file.so>         Open a plugin and print its manifest and build info

Plugins must be built with the same Go toolchain, build flags, and pluginapi
sources as this binary. "plugin build" reproduces this binary's flags and
stamps the toolchain into the plugin; "plugin inspect" reports what it finds.
`, productName)
}

// runPluginCommand dispatches `gomodel plugin <subcommand> ...`. Usage errors
// are returned as usageError so ExitCode maps them to 2.
func runPluginCommand(ctx context.Context, productName string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, pluginUsage(productName))
		return &usageError{err: errors.New("plugin: missing subcommand (build or inspect)")}
	}
	switch args[0] {
	case "build":
		opts, err := parsePluginBuildArgs(productName, args[1:], stderr)
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return &usageError{err: err}
		}
		return pluginBuild(ctx, opts, stdout, stderr)
	case "inspect":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
				fmt.Fprint(stdout, pluginUsage(productName))
				return nil
			}
			return &usageError{err: errors.New("plugin inspect: expected exactly one argument: the .so file")}
		}
		return pluginInspect(args[1], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, pluginUsage(productName))
		return nil
	}
	fmt.Fprint(stderr, pluginUsage(productName))
	return &usageError{err: fmt.Errorf("plugin: unknown subcommand %q (build or inspect)", args[0])}
}

type pluginBuildOptions struct {
	// Dir is the plugin's package main directory.
	Dir string
	// Out is the output path; default <basename of Dir>.so in the working directory.
	Out string
}

// parsePluginBuildArgs accepts flags before or after the directory argument.
func parsePluginBuildArgs(productName string, args []string, stderr io.Writer) (pluginBuildOptions, error) {
	var opts pluginBuildOptions
	fs := flag.NewFlagSet(productName+" plugin build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.Out, "o", "", "Output file (default: <dir name>.so in the current directory)")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return opts, errors.New("plugin build: missing plugin directory argument")
	}
	opts.Dir = rest[0]
	// Flags may follow the directory: parse the remainder with the same set.
	if err := fs.Parse(rest[1:]); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("plugin build: unexpected arguments: %v", fs.Args())
	}
	if opts.Out == "" {
		abs, err := filepath.Abs(opts.Dir)
		if err != nil {
			return opts, err
		}
		opts.Out = filepath.Base(abs) + ".so"
	}
	return opts, nil
}

// pluginBuild runs go build -buildmode=plugin for the directory with this
// binary's build flags, adding a generated GoModelBuildInfo declaration
// through a -overlay unless the package declares one itself.
func pluginBuild(ctx context.Context, opts pluginBuildOptions, stdout, stderr io.Writer) error {
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("plugin build: %s is not a directory", dir)
	}
	out, err := filepath.Abs(opts.Out)
	if err != nil {
		return err
	}
	host := pluginload.HostBuildInfo
	flags := pluginload.HostBuildFlags()
	fmt.Fprintf(stdout, "host: %s, pluginapi %s, flags %s\n", host.GoVersion, host.PluginAPIVersion, flags)

	args := append([]string{"build", "-buildmode=plugin"}, flags.Args()...)

	declared, err := declaresBuildInfo(dir)
	if err != nil {
		return fmt.Errorf("plugin build: %w", err)
	}
	if !declared {
		overlay, cleanup, err := writeBuildInfoOverlay(dir, host)
		if err != nil {
			return fmt.Errorf("plugin build: %w", err)
		}
		defer cleanup()
		args = append(args, "-overlay", overlay)
	}
	args = append(args, "-o", out, ".")

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = buildEnv(host.GoVersion)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("plugin build: go %s failed: %w", strings.Join(args, " "), err)
	}
	if err := checkBuiltToolchain(out, host.GoVersion); err != nil {
		return fmt.Errorf("plugin build: %w", err)
	}
	fmt.Fprintf(stdout, "built %s\n", out)
	return nil
}

// buildEnv forces cgo on and pins the Go toolchain to the host's version so
// the go command downloads the matching release when the local one differs.
// An explicit GOTOOLCHAIN in the environment is respected.
func buildEnv(goVersion string) []string {
	env := append(os.Environ(), "CGO_ENABLED=1")
	if _, set := os.LookupEnv("GOTOOLCHAIN"); !set && releaseVersion.MatchString(goVersion) {
		env = append(env, "GOTOOLCHAIN="+goVersion)
	}
	return env
}

var releaseVersion = regexp.MustCompile(`^go\d+\.\d+(\.\d+)?$`)

// checkBuiltToolchain reads the build info embedded in the output and refuses
// a plugin whose Go version differs from the host's: such a file would fail
// at load time with a far less readable error.
func checkBuiltToolchain(out, hostGo string) error {
	bi, err := buildinfo.ReadFile(out)
	if err != nil {
		return nil // not a Go binary we can introspect; the loader will report it
	}
	if bi.GoVersion != hostGo {
		return fmt.Errorf("%s was built with %s but this binary uses %s; install the matching toolchain (or let GOTOOLCHAIN=%s download it) and rebuild", out, bi.GoVersion, hostGo, hostGo)
	}
	return nil
}

// declaresBuildInfo reports whether a non-test Go file in dir declares a
// package-level GoModelBuildInfo.
func declaresBuildInfo(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return false, err
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range vs.Names {
					if ident.Name == pluginload.BuildInfoSymbol {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

// buildInfoSource is the generated declaration added to the plugin's main
// package.
func buildInfoSource(info pluginapi.BuildInfo) string {
	return fmt.Sprintf(`// Code generated by gomodel plugin build. DO NOT EDIT.

package main

import "github.com/enterpilot/gomodel/pluginapi"

// GoModelBuildInfo records the toolchain this plugin was built with. GoModel
// reads it to explain a refused load.
var GoModelBuildInfo = pluginapi.BuildInfo{GoVersion: %q, PluginAPIVersion: %q}
`, info.GoVersion, info.PluginAPIVersion)
}

// overlayJSON renders a go build -overlay file mapping the virtual path of the
// generated file inside dir to its real location.
func overlayJSON(dir, generated string) string {
	return fmt.Sprintf("{\"Replace\": {%q: %q}}", filepath.Join(dir, buildInfoFile), generated)
}

// writeBuildInfoOverlay writes the generated source and the overlay JSON to a
// temporary directory and returns the overlay path plus a cleanup function.
func writeBuildInfoOverlay(dir string, info pluginapi.BuildInfo) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "gomodel-plugin-build-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	generated := filepath.Join(tmp, buildInfoFile)
	if err := os.WriteFile(generated, []byte(buildInfoSource(info)), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	overlay := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(overlay, []byte(overlayJSON(dir, generated)), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	return overlay, cleanup, nil
}

// pluginInspect opens the shared object and prints its manifest.
func pluginInspect(path string, stdout io.Writer) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	host := pluginload.HostBuildInfo
	fmt.Fprintf(stdout, "host: %s, pluginapi %s, flags %s\n", host.GoVersion, host.PluginAPIVersion, pluginload.HostBuildFlags())
	loaded, err := pluginload.Open(abs)
	if err != nil {
		return err
	}
	writeManifest(stdout, loaded)
	return nil
}

func writeManifest(w io.Writer, l pluginload.Loaded) {
	m := l.Manifest
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "file\t%s\n", l.Path)
	fmt.Fprintf(tw, "name\t%s\n", m.Name)
	fmt.Fprintf(tw, "version\t%s\n", orDash(m.Version))
	fmt.Fprintf(tw, "description\t%s\n", orDash(m.Description))
	fmt.Fprintf(tw, "kinds\t%s\n", orDash(joinKinds(m.Kinds)))
	fmt.Fprintf(tw, "mutates\t%t\n", m.Mutates)
	fmt.Fprintf(tw, "instances\t%s\n", instancesLabel(l.SingleInstance))
	if l.BuildInfo == (pluginapi.BuildInfo{}) {
		fmt.Fprintf(tw, "built with\t- (no GoModelBuildInfo; build with `gomodel plugin build`)\n")
	} else {
		fmt.Fprintf(tw, "built with\t%s, pluginapi %s\n", orDash(l.BuildInfo.GoVersion), orDash(l.BuildInfo.PluginAPIVersion))
	}
	if len(m.ConfigSchema) == 0 {
		fmt.Fprintf(tw, "config\t-\n")
	}
	_ = tw.Flush()
	if len(m.ConfigSchema) == 0 {
		return
	}
	fmt.Fprintln(w, "config:")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  KEY\tINPUT\tREQUIRED\tDEFAULT\tSCOPE\tLABEL\n")
	for _, f := range m.ConfigSchema {
		input := f.Input
		if input == "" {
			input = pluginapi.InputText
		}
		scope := "instance"
		if f.Scope == pluginapi.ScopeRoute {
			scope = "route"
		}
		def := "-"
		if f.Default != nil {
			def = fmt.Sprint(f.Default)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%t\t%s\t%s\t%s\n", f.Key, input, f.Required, def, scope, f.Label)
	}
	_ = tw.Flush()
}

func joinKinds(kinds []pluginapi.Kind) string {
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

func instancesLabel(single bool) string {
	if single {
		return "one (GoModelPlugin is a variable)"
	}
	return "many (GoModelPlugin is a constructor)"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
