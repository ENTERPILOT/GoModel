package plugins

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Factory builds a fresh plugin value. Every configured instance gets its own
// value so plugins can keep per-instance state.
type Factory func() pluginapi.Plugin

// Source says where a plugin type came from: compiled into the binary, added
// through the ext registry, or loaded from a shared object path.
type Source string

const (
	SourceBuiltin    Source = "builtin"
	SourceRegistered Source = "registered"
)

// Entry is one plugin type known to the catalog.
type Entry struct {
	Name     string
	Manifest pluginapi.Manifest
	// Kinds are the hooks the plugin declares and actually implements.
	Kinds   []pluginapi.Kind
	Source  Source
	Factory Factory
	// SingleInstance marks a type that can back only one configured instance
	// (a shared object exporting a plugin variable rather than a constructor).
	SingleInstance bool
	// Health is "ok" for a usable entry and "error" for one that failed to
	// load; Err carries the failure.
	Health string
	Err    error
}

// RegisterOptions tunes Register.
type RegisterOptions struct {
	SingleInstance bool
}

// HasKind reports whether the entry implements the hook.
func (e Entry) HasKind(kind pluginapi.Kind) bool {
	return hasKind(e.Kinds, kind)
}

// Catalog is the process-wide set of plugin types.
type Catalog struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{entries: map[string]Entry{}}
}

// Register probes factory once, validates its manifest, and adds it under the
// manifest name. Every Kind the manifest declares must be backed by the
// matching hook interface on the probed value.
func (c *Catalog) Register(factory Factory, source Source, options ...RegisterOptions) (err error) {
	var opts RegisterOptions
	if len(options) > 0 {
		opts = options[0]
	}
	if c == nil {
		return fmt.Errorf("plugins: catalog is required")
	}
	if factory == nil {
		return fmt.Errorf("plugins: factory is required")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugins: plugin factory panicked: %v", r)
		}
	}()
	probe := factory()
	if probe == nil {
		return fmt.Errorf("plugins: factory returned nil plugin")
	}
	manifest := probe.Manifest()
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		return fmt.Errorf("plugins: manifest name is required")
	}
	if strings.ContainsAny(name, "/:") {
		return fmt.Errorf("plugins: manifest name %q cannot contain '/' or ':'", name)
	}
	manifest.Name = name
	kinds, err := validateKinds(name, manifest.Kinds, probe)
	if err != nil {
		return err
	}
	if err := validateSchema(name, manifest.ConfigSchema); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[name]; ok && existing.Err == nil {
		return fmt.Errorf("plugins: duplicate plugin name %q (already registered from %s)", name, existing.Source)
	}
	c.entries[name] = Entry{
		Name:     name,
		Manifest: manifest,
		Kinds:    kinds,
		Source:   source,
		Factory:  factory,
		Health:   "ok",

		SingleInstance: opts.SingleInstance,
	}
	return nil
}

// RegisterFailed records a plugin that could not be loaded so the admin API
// can report it. A later successful Register under the same name replaces it.
func (c *Catalog) RegisterFailed(name string, source Source, err error) {
	if c == nil || err == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = string(source)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[name]; ok && existing.Err == nil {
		return
	}
	c.entries[name] = Entry{Name: name, Source: source, Health: "error", Err: err}
}

// Lookup returns the usable entry registered under name.
func (c *Catalog) Lookup(name string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[strings.TrimSpace(name)]
	if !ok || entry.Err != nil {
		return Entry{}, false
	}
	return entry, true
}

// Entries returns every entry, failed loads included, sorted by name.
func (c *Catalog) Entries() []Entry {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make([]Entry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// Names returns the usable plugin names sorted.
func (c *Catalog) Names() []string {
	var names []string
	for _, entry := range c.Entries() {
		if entry.Err == nil {
			names = append(names, entry.Name)
		}
	}
	return names
}

// Len returns the number of usable plugin types.
func (c *Catalog) Len() int {
	return len(c.Names())
}

// ImplementedKinds reports the hook interfaces p satisfies.
func ImplementedKinds(p pluginapi.Plugin) []pluginapi.Kind {
	var kinds []pluginapi.Kind
	if _, ok := p.(pluginapi.RequestHook); ok {
		kinds = append(kinds, pluginapi.KindRequest)
	}
	if _, ok := p.(pluginapi.PromptHook); ok {
		kinds = append(kinds, pluginapi.KindPrompt)
	}
	if _, ok := p.(pluginapi.ResponseHook); ok {
		kinds = append(kinds, pluginapi.KindResponse)
	}
	if _, ok := p.(pluginapi.StreamHook); ok {
		kinds = append(kinds, pluginapi.KindStream)
	}
	if _, ok := p.(pluginapi.RouteStrategy); ok {
		kinds = append(kinds, pluginapi.KindRoute)
	}
	if _, ok := p.(pluginapi.CompleteHook); ok {
		kinds = append(kinds, pluginapi.KindComplete)
	}
	return kinds
}

// validateKinds checks every declared kind against the probe and returns the
// effective kind list. A manifest that declares nothing gets the implemented
// hooks.
func validateKinds(name string, declared []pluginapi.Kind, probe pluginapi.Plugin) ([]pluginapi.Kind, error) {
	implemented := ImplementedKinds(probe)
	if len(declared) == 0 {
		return implemented, nil
	}
	kinds := make([]pluginapi.Kind, 0, len(declared))
	for _, kind := range declared {
		if hasKind(kinds, kind) {
			continue
		}
		if !hasKind(implemented, kind) {
			return nil, fmt.Errorf("plugins: plugin %q declares kind %q but does not implement its hook", name, kind)
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func validateSchema(name string, schema []pluginapi.Field) error {
	seen := make(map[string]struct{}, len(schema))
	for _, field := range schema {
		key := strings.TrimSpace(field.Key)
		if key == "" {
			return fmt.Errorf("plugins: plugin %q has a config field without a key", name)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("plugins: plugin %q declares config field %q twice", name, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func hasKind(kinds []pluginapi.Kind, kind pluginapi.Kind) bool {
	return slices.Contains(kinds, kind)
}

// PhaseKinds are the hook kinds a workflow step can reference.
var PhaseKinds = []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream}

// IsPhaseKind reports whether kind is a workflow phase.
func IsPhaseKind(kind pluginapi.Kind) bool {
	return hasKind(PhaseKinds, kind)
}
