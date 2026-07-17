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
// A non-empty value always wins, canonical first. An empty canonical does not
// shadow a working legacy value: `GOMODEL_SQLITE_PATH=` (an unexpanded compose
// variable, say) alongside a real SQLITE_PATH resolves to the real one rather
// than silently discarding it. Presence is still reported when only empty
// values are set, so callers that use the bool to detect an explicit ""
// keep working.
//
// Names that are exempt or already carry the prefix are read as given.
func Lookup(name string) (string, bool) {
	if exempt[name] || strings.HasPrefix(name, Prefix) {
		return os.LookupEnv(name)
	}

	canonical := Prefix + name
	canonicalValue, canonicalSet := os.LookupEnv(canonical)
	if canonicalSet && canonicalValue != "" {
		return canonicalValue, true
	}

	legacyValue, legacySet := os.LookupEnv(name)
	if legacySet && legacyValue != "" {
		warn(name, canonical)
		return legacyValue, true
	}

	switch {
	case canonicalSet:
		return "", true
	case legacySet:
		warn(name, canonical)
		return "", true
	default:
		return "", false
	}
}

// Get returns the value of name, or "" when neither spelling is set.
func Get(name string) string {
	value, _ := Lookup(name)
	return value
}

// Entry is one variable found by Scan.
type Entry struct {
	// Name is the legacy (unprefixed) spelling of the variable, regardless of
	// which spelling was actually set. Callers pass it back to Lookup to
	// resolve companion variables, so a canonical entry with a legacy
	// companion — or the reverse — resolves correctly.
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
// When both spellings of the same suffix are set, the canonical one wins and
// the legacy one is ignored — matching Lookup's precedence.
//
// Entries are sorted by suffix. Callers resolve suffixes to canonical keys and
// two suffixes can collide there (SET_RATE_LIMIT_A_ and SET_RATE_LIMIT_A both
// name path "a"), so iteration order decides which one wins; sorting keeps that
// resolution from depending on the order the OS happens to return.
func Scan(prefix string) []Entry {
	canonicalPrefix := Prefix + prefix

	bySuffix := make(map[string]Entry)
	legacy := make(map[string]string)

	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(key, canonicalPrefix):
			suffix := key[len(canonicalPrefix):]
			bySuffix[suffix] = Entry{Name: prefix + suffix, Suffix: suffix, Value: value}
		case strings.HasPrefix(key, prefix):
			legacy[key[len(prefix):]] = value
		}
	}

	for suffix, value := range legacy {
		if _, canonicalSet := bySuffix[suffix]; canonicalSet {
			continue
		}
		name := prefix + suffix
		warn(name, Prefix+name)
		bySuffix[suffix] = Entry{Name: name, Suffix: suffix, Value: value}
	}

	entries := make([]Entry, 0, len(bySuffix))
	for _, entry := range bySuffix {
		entries = append(entries, entry)
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
