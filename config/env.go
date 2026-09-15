package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// applyEnvOverrides walks cfg's struct fields and applies env var overrides
// based on `env` struct tags. Maps are skipped.
func applyEnvOverrides(cfg *Config) error {
	if err := applyEnvOverridesValue(reflect.ValueOf(cfg).Elem()); err != nil {
		return err
	}
	normalizeModelListURL(cfg)
	applyOfflineMode(cfg)
	applyPluginsLoadEnv(cfg)
	return nil
}

// parsePluginLoadEntry parses one PLUGINS_LOAD item. The last "=" separates
// file from digest only when the suffix is exactly 64 hex characters (a
// sha256); anything else — including "=" or "," inside a file name — makes
// the whole item the file name. Commas delimit entries by design, so file
// names containing commas are not supported.
func parsePluginLoadEntry(item string) PluginFileConfig {
	item = strings.TrimSpace(item)
	i := strings.LastIndex(item, "=")
	if i < 0 {
		return PluginFileConfig{File: item}
	}
	if sha := item[i+1:]; isSHA256Hex(sha) {
		return PluginFileConfig{File: strings.TrimSpace(item[:i]), SHA256: sha}
	}
	return PluginFileConfig{File: item}
}

// isSHA256Hex reports whether s is exactly 64 hex characters.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// applyPluginsLoadEnv applies PLUGINS_LOAD, replacing plugins.load from the
// config file, so env > config for the plugin list like for every other
// env-configurable value. The generic env overlay cannot express a list of
// structs, so this variable is applied explicitly after the struct walk —
// the same pattern as the existing normalizeModelListURL. The value is a
// comma-separated list of .so file names, each entry optionally
// "file=sha256hex" to pin the digest.
func applyPluginsLoadEnv(cfg *Config) {
	v := strings.TrimSpace(os.Getenv("PLUGINS_LOAD"))
	if v == "" {
		return
	}
	load := make([]PluginFileConfig, 0, 4)
	for _, item := range strings.Split(v, ",") {
		if entry := parsePluginLoadEntry(item); entry.File != "" {
			load = append(load, entry)
		}
	}
	cfg.Plugins.Load = load
}

// applyOfflineMode enforces the offline switch after every other source has
// been applied, so neither config.yaml nor an env var can re-enable an
// outbound call underneath it. A model catalog read from the local
// filesystem is kept: it makes no network request.
func applyOfflineMode(cfg *Config) {
	if !cfg.Offline {
		return
	}
	cfg.VersionCheck.Enabled = false
	if url := cfg.Cache.Model.ModelList.URL; url != "" && !IsLocalModelListSource(url) {
		cfg.Cache.Model.ModelList.URL = ""
	}
}

// IsLocalModelListSource reports whether a model list location names a file
// on the local filesystem ("file://..." or a bare path) rather than an HTTP
// URL. It mirrors what the catalog fetcher accepts.
func IsLocalModelListSource(location string) bool {
	location = strings.TrimSpace(location)
	if location == "" {
		return false
	}
	lower := strings.ToLower(location)
	if strings.HasPrefix(lower, "file://") {
		return true
	}
	return !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")
}

// normalizeModelListURL maps the sentinel "off" (case-insensitive) to an empty
// model list URL, disabling catalog downloads for air-gapped installs. A
// sentinel is used because empty env values are skipped by the generic overlay
// (so MODEL_LIST_URL="" cannot override the default), and it works identically
// when set via config.yaml.
func normalizeModelListURL(cfg *Config) {
	if strings.EqualFold(strings.TrimSpace(cfg.Cache.Model.ModelList.URL), "off") {
		cfg.Cache.Model.ModelList.URL = ""
	}
}

// hasEnvDescendants reports whether t (a struct type) contains any field (at
// any depth) with a non-empty "env" struct tag. Used to decide whether to
// allocate a nil pointer-to-struct before recursing into it.
func hasEnvDescendants(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for f := range t.Fields() {
		if f.Tag.Get("env") != "" {
			return true
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && hasEnvDescendants(ft) {
			return true
		}
	}
	return false
}

func applyEnvOverridesValue(v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldVal := v.Field(i)

		if field.Type.Kind() == reflect.Map {
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			if err := applyEnvOverridesValue(fieldVal); err != nil {
				return err
			}
			continue
		}
		if field.Type.Kind() == reflect.Pointer {
			elemType := field.Type.Elem()
			if elemType.Kind() != reflect.Struct {
				continue
			}
			if fieldVal.IsNil() {
				// Only allocate if the pointed-to struct has env-tagged descendants;
				// otherwise leave it nil so optional config sections stay absent.
				if !hasEnvDescendants(elemType) {
					continue
				}
				// Allocate a zero-value struct so env vars can populate its fields.
				newVal := reflect.New(elemType)
				if err := applyEnvOverridesValue(newVal.Elem()); err != nil {
					return err
				}
				// Only keep the allocation if at least one field was actually set.
				if !reflect.DeepEqual(newVal.Elem().Interface(), reflect.Zero(elemType).Interface()) {
					fieldVal.Set(newVal)
				}
			} else {
				if err := applyEnvOverridesValue(fieldVal.Elem()); err != nil {
					return err
				}
			}
			continue
		}

		envKey := field.Tag.Get("env")
		if envKey == "" {
			continue
		}
		envVal := os.Getenv(envKey)
		if envVal == "" {
			continue
		}

		switch field.Type.Kind() {
		case reflect.String:
			fieldVal.SetString(envVal)
		case reflect.Bool:
			fieldVal.SetBool(parseBool(envVal))
		case reflect.Slice:
			if field.Type.Elem().Kind() != reflect.String {
				continue
			}
			items := strings.Split(envVal, ",")
			values := make([]string, 0, len(items))
			for _, item := range items {
				trimmed := strings.TrimSpace(item)
				if trimmed == "" {
					continue
				}
				values = append(values, trimmed)
			}
			fieldVal.Set(reflect.ValueOf(values))
		case reflect.Int:
			n, err := strconv.Atoi(envVal)
			if err != nil {
				return fmt.Errorf("invalid value for %s (%s): %q is not a valid integer", field.Name, envKey, envVal)
			}
			fieldVal.SetInt(int64(n))
		case reflect.Int64:
			if field.Type == reflect.TypeFor[time.Duration]() {
				// time.Duration is represented as int64; accept Go duration strings (e.g. "1s", "500ms").
				d, err := time.ParseDuration(envVal)
				if err != nil {
					return fmt.Errorf("invalid value for %s (%s): %q is not a valid duration", field.Name, envKey, envVal)
				}
				fieldVal.SetInt(int64(d))
			} else {
				n, err := strconv.ParseInt(envVal, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid value for %s (%s): %q is not a valid integer", field.Name, envKey, envVal)
				}
				fieldVal.SetInt(n)
			}
		case reflect.Float64:
			f, err := strconv.ParseFloat(envVal, 64)
			if err != nil {
				return fmt.Errorf("invalid value for %s (%s): %q is not a valid float", field.Name, envKey, envVal)
			}
			fieldVal.SetFloat(f)
		}
	}
	return nil
}

// expandString expands environment variable references like ${VAR} or ${VAR:-default} in a string.
func expandString(s string) string {
	if s == "" {
		return s
	}
	return os.Expand(s, func(key string) string {
		varname := key
		defaultValue := ""
		hasDefault := false
		if before, after, ok := strings.Cut(key, ":-"); ok {
			varname = before
			defaultValue = after
			hasDefault = true
		}
		value := os.Getenv(varname)
		if value == "" {
			if hasDefault {
				return defaultValue
			}
			return "${" + key + "}"
		}
		return value
	})
}

// parseBool returns true if s is "true" or "1" (case-insensitive).
func parseBool(s string) bool {
	return strings.EqualFold(s, "true") || s == "1"
}
