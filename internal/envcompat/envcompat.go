// Package envcompat resolves GoModel's environment variables during the
// migration to the GOMODEL_ prefix.
//
// Every variable GoModel defines is canonically spelled GOMODEL_<NAME>. The
// unprefixed spelling is still accepted so existing deployments keep working,
// but it is deprecated and warns once per variable.
//
// Variables that GoModel does not define are exempt and read bare: PORT and
// REDIS_URL are injected by PaaS platforms, and the provider family
// (OPENAI_API_KEY, <PROVIDER>_BASE_URL, ...) lives in each vendor's namespace,
// which is what makes GoModel drop-in compatible with their SDKs. The provider
// family never routes through this package; only PORT and REDIS_URL need the
// exempt set, because the struct-tag walker resolves every tagged field here.
package envcompat

import (
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
)

// Prefix is the canonical namespace for GoModel-defined variables.
const Prefix = "GOMODEL_"

// exempt lists variables that keep their bare spelling forever. They are named
// by conventions GoModel does not own, so prefixing them would break the
// interop that makes the bare name worth reading in the first place.
var exempt = map[string]bool{
	"PORT":      true,
	"REDIS_URL": true,
}

// warned dedupes deprecation warnings so a variable read on a hot path cannot
// spam the log.
var warned sync.Map

// Lookup returns the value of name, preferring the canonical GOMODEL_-prefixed
// spelling and falling back to the deprecated bare spelling. The bool reports
// whether either spelling was set, distinguishing an unset variable from one
// explicitly set to the empty string.
//
// A value with non-whitespace content always wins, canonical first. A blank
// canonical does not shadow a working legacy value: `GOMODEL_SQLITE_PATH=` or
// `GOMODEL_SQLITE_PATH=" "` (an unexpanded compose variable, say) alongside a
// real SQLITE_PATH resolves to the real one rather than silently discarding
// it. When only blank values are set, presence is still reported with the raw
// value, so callers that use the bool to detect an explicit "" keep working.
//
// Names that are exempt or already carry the prefix are read as given.
func Lookup(name string) (string, bool) {
	value, ok, _ := lookup(name, false)
	return value, ok
}

// Get returns the value of name, or "" when neither spelling is set.
func Get(name string) string {
	value, _ := Lookup(name)
	return value
}

// Quiet returns the value of name like Get, but never logs a deprecation
// warning. It exists for code that must resolve a variable before the slog
// handler is installed (the logging configuration itself); calling Get for the
// same name afterwards emits the warning through the configured handler, since
// Quiet does not consume the warn-once budget.
func Quiet(name string) string {
	value, _, _ := lookup(name, true)
	return value
}

// lookup implements the resolution shared by Lookup, Quiet, and Scan, and
// reports which spelling supplied the result so messages can name a variable
// that actually exists in the operator's environment.
func lookup(name string, quiet bool) (value string, ok bool, source string) {
	if exempt[name] {
		if _, prefixedSet := os.LookupEnv(Prefix + name); prefixedSet && !quiet {
			warnPrefixedExempt(name)
		}
		value, ok = os.LookupEnv(name)
		return value, ok, name
	}
	if strings.HasPrefix(name, Prefix) {
		value, ok = os.LookupEnv(name)
		return value, ok, name
	}

	canonical := Prefix + name
	canonicalValue, canonicalSet := os.LookupEnv(canonical)
	if canonicalSet && strings.TrimSpace(canonicalValue) != "" {
		return canonicalValue, true, canonical
	}

	legacyValue, legacySet := os.LookupEnv(name)
	if legacySet && strings.TrimSpace(legacyValue) != "" {
		if !quiet {
			warn(name, canonical)
		}
		return legacyValue, true, name
	}

	switch {
	case canonicalSet:
		return canonicalValue, true, canonical
	case legacySet:
		if !quiet {
			warn(name, canonical)
		}
		return legacyValue, true, name
	default:
		return "", false, name
	}
}

// Entry is one variable found by Scan.
type Entry struct {
	// Name is the spelling that actually supplied the value — canonical when
	// the GOMODEL_-prefixed variable won, legacy otherwise — so error messages
	// can name a variable that exists in the operator's environment.
	// Companion variables are resolved by passing their bare name to Lookup
	// or Get, which accepts either spelling.
	Name string

	// Suffix is the part of the name following the scanned prefix.
	Suffix string

	// Value is the resolved value.
	Value string
}

// Scan returns every variable whose name starts with prefix in either
// spelling, keyed by suffix. It exists for the variable families that are
// discovered by walking the environment rather than looked up by name
// (GOMODEL_SET_RATE_LIMIT_<PATH>, GOMODEL_TAGGING_HEADER_<N>, ...).
//
// Each discovered suffix is resolved through the same precedence as Lookup —
// the two cannot diverge — so the canonical spelling wins when it has content
// and a blank canonical does not shadow a working legacy value.
//
// Entries are sorted by suffix. Callers resolve suffixes to canonical keys and
// two suffixes can collide there (SET_RATE_LIMIT_A_ and SET_RATE_LIMIT_A both
// name path "a"), so iteration order decides which one wins; sorting keeps that
// resolution from depending on the order the OS happens to return.
func Scan(prefix string) []Entry {
	canonicalPrefix := Prefix + prefix

	suffixes := make(map[string]bool)
	for _, kv := range os.Environ() {
		key, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		switch {
		case strings.HasPrefix(key, canonicalPrefix):
			suffixes[key[len(canonicalPrefix):]] = true
		case strings.HasPrefix(key, prefix):
			suffixes[key[len(prefix):]] = true
		}
	}

	entries := make([]Entry, 0, len(suffixes))
	for suffix := range suffixes {
		value, ok, source := lookup(prefix+suffix, false)
		if !ok {
			continue
		}
		entries = append(entries, Entry{Name: source, Suffix: suffix, Value: value})
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		return strings.Compare(a.Suffix, b.Suffix)
	})
	return entries
}

func warn(legacy, canonical string) {
	if _, seen := warned.LoadOrStore(legacy, struct{}{}); seen {
		return
	}
	slog.Warn("deprecated environment variable: rename it before the next major release",
		"variable", legacy,
		"use", canonical,
	)
}

// warnPrefixedExempt fires once when the GOMODEL_-prefixed spelling of an
// exempt name is set. The prefixed name is never read, so an operator who
// prefixed their whole env block mechanically would otherwise get a silent
// misconfiguration (GOMODEL_PORT=9090 booting on 8080).
func warnPrefixedExempt(name string) {
	prefixed := Prefix + name
	if _, seen := warned.LoadOrStore(prefixed, struct{}{}); seen {
		return
	}
	slog.Warn("environment variable is not read: this name is platform-injected and stays bare",
		"variable", prefixed,
		"use", name,
	)
}
