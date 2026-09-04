package pluginload

import (
	"errors"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Symbol names a shared object exports for GoModel.
const (
	// PluginSymbol is required: `func GoModelPlugin() pluginapi.Plugin`
	// (preferred, one file can back several instances) or
	// `var GoModelPlugin pluginapi.Plugin` (single instance).
	PluginSymbol = "GoModelPlugin"
	// BuildInfoSymbol is optional: `var GoModelBuildInfo pluginapi.BuildInfo`,
	// stamped by `gomodel plugin build`.
	BuildInfoSymbol = "GoModelBuildInfo"
)

// Open opens one shared object, resolves its symbols, and reads its manifest
// from a probe instance. path should be absolute.
func Open(path string) (Loaded, error) {
	if !Supported {
		return Loaded{}, fmt.Errorf("plugin file %s: %w", path, errUnsupported())
	}
	syms, err := openSymbols(path)
	if err != nil {
		return Loaded{}, describeOpenError(path, err)
	}
	loaded := Loaded{Path: path}
	loaded.Factory, loaded.SingleInstance, err = factoryFromSymbol(syms.plugin)
	if err != nil {
		return Loaded{}, fmt.Errorf("plugin file %s: %w", path, err)
	}
	if syms.buildInfo != nil {
		loaded.BuildInfo, err = buildInfoFromSymbol(syms.buildInfo)
		if err != nil {
			return Loaded{}, fmt.Errorf("plugin file %s: %w", path, err)
		}
	}
	loaded.Manifest, err = probeManifest(loaded.Factory)
	if err != nil {
		return Loaded{}, fmt.Errorf("plugin file %s: %w", path, err)
	}
	if loaded.Manifest.BuiltWith == (pluginapi.BuildInfo{}) {
		loaded.Manifest.BuiltWith = loaded.BuildInfo
	}
	return loaded, nil
}

// symbols is what openSymbols finds in a shared object. Values are whatever
// plugin.Lookup returned: a func value for functions, a pointer for
// variables.
type symbols struct {
	plugin    any
	buildInfo any // nil when the symbol is absent
}

func factoryFromSymbol(sym any) (factory func() pluginapi.Plugin, single bool, err error) {
	switch s := sym.(type) {
	case func() pluginapi.Plugin:
		return s, false, nil
	case *func() pluginapi.Plugin:
		if s == nil || *s == nil {
			return nil, false, fmt.Errorf("symbol %s is a nil constructor", PluginSymbol)
		}
		return *s, false, nil
	case *pluginapi.Plugin:
		if s == nil || *s == nil {
			return nil, false, fmt.Errorf("symbol %s is a nil plugin value", PluginSymbol)
		}
		p := *s
		return func() pluginapi.Plugin { return p }, true, nil
	}
	return nil, false, fmt.Errorf("symbol %s has type %T; want func() pluginapi.Plugin or a pluginapi.Plugin variable", PluginSymbol, sym)
}

func buildInfoFromSymbol(sym any) (pluginapi.BuildInfo, error) {
	switch s := sym.(type) {
	case *pluginapi.BuildInfo:
		if s == nil {
			return pluginapi.BuildInfo{}, nil
		}
		return *s, nil
	case **pluginapi.BuildInfo:
		if s == nil || *s == nil {
			return pluginapi.BuildInfo{}, nil
		}
		return **s, nil
	}
	return pluginapi.BuildInfo{}, fmt.Errorf("symbol %s has type %T; want pluginapi.BuildInfo", BuildInfoSymbol, sym)
}

// probeManifest builds one instance and reads its manifest, converting a
// panic in the plugin into an error. It also checks that every declared Kind
// is backed by the matching hook interface.
func probeManifest(factory func() pluginapi.Plugin) (m pluginapi.Manifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panicked while reading its manifest: %v", r)
		}
	}()
	p := factory()
	if p == nil {
		return m, fmt.Errorf("%s returned a nil plugin", PluginSymbol)
	}
	m = p.Manifest()
	if strings.TrimSpace(m.Name) == "" {
		return m, errors.New("manifest has an empty Name")
	}
	if err := checkKinds(p, m.Kinds); err != nil {
		return m, fmt.Errorf("plugin %q: %w", m.Name, err)
	}
	return m, nil
}

func checkKinds(p pluginapi.Plugin, kinds []pluginapi.Kind) error {
	for _, kind := range kinds {
		var ok bool
		switch kind {
		case pluginapi.KindRequest:
			_, ok = p.(pluginapi.RequestHook)
		case pluginapi.KindPrompt:
			_, ok = p.(pluginapi.PromptHook)
		case pluginapi.KindResponse:
			_, ok = p.(pluginapi.ResponseHook)
		case pluginapi.KindStream:
			_, ok = p.(pluginapi.StreamHook)
		case pluginapi.KindRoute:
			_, ok = p.(pluginapi.RouteStrategy)
		case pluginapi.KindComplete:
			_, ok = p.(pluginapi.CompleteHook)
		default:
			return fmt.Errorf("manifest lists unknown kind %q", kind)
		}
		if !ok {
			return fmt.Errorf("manifest lists kind %q but the plugin does not implement the %s interface", kind, hookInterface(kind))
		}
	}
	return nil
}

func hookInterface(kind pluginapi.Kind) string {
	switch kind {
	case pluginapi.KindRequest:
		return "pluginapi.RequestHook"
	case pluginapi.KindPrompt:
		return "pluginapi.PromptHook"
	case pluginapi.KindResponse:
		return "pluginapi.ResponseHook"
	case pluginapi.KindStream:
		return "pluginapi.StreamHook"
	case pluginapi.KindRoute:
		return "pluginapi.RouteStrategy"
	case pluginapi.KindComplete:
		return "pluginapi.CompleteHook"
	}
	return string(kind)
}

func errUnsupported() error {
	return fmt.Errorf("shared object plugins are not supported by this binary (%s): Go plugins need CGO_ENABLED=1 on linux, darwin, or freebsd; use the gomodel:<version>-plugins image or `make build-plugins`", hostDescription())
}

func hostDescription() string {
	return fmt.Sprintf("host %s, pluginapi %s, flags %s", HostBuildInfo.GoVersion, HostBuildInfo.PluginAPIVersion, HostBuildFlags())
}
