package config

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// parsedEnvVars are the variables Load reads outside the env struct tags and
// parses into a number, boolean, or list.
var parsedEnvVars = []string{
	envConfigStrict,
	"PLUGINS_LOAD",
	"RESPONSE_CACHE_SIMPLE_ENABLED",
	"REDIS_TTL_RESPONSES",
	"SEMANTIC_CACHE_ENABLED",
	"SEMANTIC_CACHE_THRESHOLD",
	"SEMANTIC_CACHE_TTL",
	"SEMANTIC_CACHE_MAX_CONV_MESSAGES",
	"SEMANTIC_CACHE_EXCLUDE_SYSTEM_PROMPT",
	"SEMANTIC_CACHE_PGVECTOR_DIMENSION",
	"SEMANTIC_CACHE_PINECONE_DIMENSION",
}

// parsedEnvPrefixes are variable families whose values are limits.
var parsedEnvPrefixes = []string{"SET_BUDGET_", "SET_RATE_LIMIT_", "SET_PROVIDER_RATE_LIMIT_"}

// parsedEnvNames returns every variable Load parses into something other than
// a single string: the env-tagged non-string fields of Config plus
// parsedEnvVars.
var parsedEnvNames = sync.OnceValue(func() map[string]struct{} {
	names := make(map[string]struct{}, len(parsedEnvVars))
	for _, name := range parsedEnvVars {
		names[name] = struct{}{}
	}
	collectParsedEnvTags(reflect.TypeFor[Config](), names)
	return names
})

func collectParsedEnvTags(t reflect.Type, names map[string]struct{}) {
	for field := range t.Fields() {
		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			collectParsedEnvTags(ft, names)
			continue
		}
		if key := field.Tag.Get("env"); key != "" && ft.Kind() != reflect.String {
			names[key] = struct{}{}
		}
	}
}

// rejectParsedEnvReferences fails when a variable that Load parses into a
// number, boolean, duration, or list holds a secret reference. Load parses
// those values before references are resolved, and before an extension can
// register its scheme, so the reference would be misread rather than used.
// Strings, including the string values inside JSON variables, accept
// references.
func rejectParsedEnvReferences(environ []string) error {
	names := parsedEnvNames()
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		if !HasSecretReference(value) || !isParsedEnvName(names, key) {
			continue
		}
		return fmt.Errorf("%s: secret references are supported only in string settings; this variable is parsed as a number, boolean, duration, or list", key)
	}
	return nil
}

func isParsedEnvName(names map[string]struct{}, key string) bool {
	if _, ok := names[key]; ok {
		return true
	}
	if strings.HasPrefix(key, "TAGGING_HEADER_") && strings.HasSuffix(key, "_DONOTPASS") {
		return true
	}
	for _, prefix := range parsedEnvPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
