package pluginload

import (
	"debug/buildinfo"
	"fmt"
	"runtime/debug"
	"strings"
)

// hostModule is the module path plugins share with this binary through the
// pluginapi package.
const hostModule = "github.com/enterpilot/gomodel"

// BuildFlags are the go build flags that change compiled package hashes and
// therefore must match between a host and its plugins.
type BuildFlags struct {
	Trimpath bool
	Race     bool
	Tags     string
	GCFlags  string
	ASMFlags string
}

// Args returns the flags as go build arguments.
func (f BuildFlags) Args() []string {
	var args []string
	if f.Trimpath {
		args = append(args, "-trimpath")
	}
	if f.Race {
		args = append(args, "-race")
	}
	if f.Tags != "" {
		args = append(args, "-tags="+f.Tags)
	}
	if f.GCFlags != "" {
		args = append(args, "-gcflags="+f.GCFlags)
	}
	if f.ASMFlags != "" {
		args = append(args, "-asmflags="+f.ASMFlags)
	}
	return args
}

// String renders the flags for diagnostics; "(none)" when empty.
func (f BuildFlags) String() string {
	args := f.Args()
	if len(args) == 0 {
		return "(none)"
	}
	return strings.Join(args, " ")
}

func flagsFromSettings(settings []debug.BuildSetting) BuildFlags {
	var f BuildFlags
	for _, s := range settings {
		switch s.Key {
		case "-trimpath":
			f.Trimpath = s.Value == "true"
		case "-race":
			f.Race = s.Value == "true"
		case "-tags":
			f.Tags = s.Value
		case "-gcflags":
			f.GCFlags = s.Value
		case "-asmflags":
			f.ASMFlags = s.Value
		}
	}
	return f
}

// HostBuildFlags returns the flags this binary was built with, read from its
// embedded build info. Plugins must be built with the same flags.
func HostBuildFlags() BuildFlags {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return BuildFlags{}
	}
	return flagsFromSettings(bi.Settings)
}

// hostModuleVersion returns the version of the gomodel module compiled into
// this binary: the main module version for the gomodel binary, or the
// dependency version for custom distributions built on the run package.
func hostModuleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return moduleVersion(bi)
}

func moduleVersion(bi *debug.BuildInfo) string {
	if bi.Main.Path == hostModule {
		return bi.Main.Version
	}
	for _, dep := range bi.Deps {
		if dep.Path == hostModule {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return ""
}

// pluginBuild is what the build info embedded in a shared object says about
// how it was built.
type pluginBuild struct {
	GoVersion     string
	ModuleVersion string
	Flags         BuildFlags
}

func (b pluginBuild) String() string {
	mod := b.ModuleVersion
	if mod == "" {
		mod = "unknown"
	}
	return fmt.Sprintf("%s, gomodel %s, flags %s", b.GoVersion, mod, b.Flags)
}

// readPluginBuild reads the Go build info embedded in a shared object without
// loading it. It works for any Go binary, including one this host refuses.
func readPluginBuild(path string) (pluginBuild, bool) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return pluginBuild{}, false
	}
	return pluginBuild{
		GoVersion:     bi.GoVersion,
		ModuleVersion: moduleVersion(bi),
		Flags:         flagsFromSettings(bi.Settings),
	}, true
}

// describeOpenError turns the plugin package's terse errors into a message
// that names the file and both toolchains.
func describeOpenError(path string, err error) error {
	msg := err.Error()
	host := fmt.Sprintf("this binary was built with %s, gomodel %s, flags %s", HostBuildInfo.GoVersion, orUnknown(hostModuleVersion()), HostBuildFlags())
	switch {
	case isMissingSymbol(err):
		return fmt.Errorf("plugin file %s does not export %s (declare `func %s() pluginapi.Plugin` in package main): %w", path, PluginSymbol, PluginSymbol, err)
	case strings.Contains(msg, "different version of package"):
		built := "its build info is unreadable"
		if b, ok := readPluginBuild(path); ok {
			built = "it was built with " + b.String()
		}
		return fmt.Errorf("plugin file %s was built with a different toolchain, flags, or pluginapi sources: %s; %s. Rebuild it with `gomodel plugin build` from this GoModel version: %w", path, built, host, err)
	case strings.Contains(msg, "not implemented"):
		return fmt.Errorf("plugin file %s: %w: %w", path, errUnsupported(), err)
	}
	return fmt.Errorf("plugin file %s: %w (%s)", path, err, host)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
