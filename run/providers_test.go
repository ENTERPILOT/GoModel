package run

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
)

func TestDefaultProviderFactoryRegistersAllProviderTypes(t *testing.T) {
	expected := []string{
		"anthropic", "azure", "bailian", "bedrock", "deepseek", "fireworks",
		"gemini", "groq", "kilo", "kimicode", "meta", "minimax", "ollama", "openai", "opencode_go",
		"openrouter", "oracle", "vertex", "vllm", "xai", "xiaomi", "zai",
	}

	for _, metricsEnabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Metrics.Enabled = metricsEnabled

		factory := defaultProviderFactory(cfg)
		got := factory.RegisteredTypes()
		slices.Sort(got)

		if !slices.Equal(got, expected) {
			t.Errorf("metrics=%v: registered types = %v, want %v", metricsEnabled, got, expected)
		}
	}
}

// TestConfigEnvTagsAvoidProviderFamilyNames pins the envcompat exemption
// contract: provider-family variables (OPENAI_API_KEY, <PROVIDER>_BASE_URL,
// <PROVIDER>_MODELS, ...) are discovered by the provider registry and read
// bare — they must never appear as `env:` struct tags, because the config
// env-tag walker resolves every tag through envcompat's GOMODEL_-prefix
// rules. A tag naming one would let GOMODEL_<PROVIDER>_... override the
// vendor spelling and log a bogus deprecation warning for a name that must
// stay bare. See the exempt table in
// docs/dev/2026-07-17_env-prefix-migration.md.
func TestConfigEnvTagsAvoidProviderFamilyNames(t *testing.T) {
	factory := defaultProviderFactory(&config.Config{})

	family := make([]*regexp.Regexp, 0)
	for _, providerType := range factory.RegisteredTypes() {
		prefix := regexp.QuoteMeta(strings.ToUpper(providerType) + "_")
		family = append(family, regexp.MustCompile(
			"^"+prefix+`(API_KEY(_[0-9]+)?|BASE_URL|MODELS|API_VERSION)$`))
	}

	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(rt.Elem(), path)
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[rt] {
			return
		}
		seen[rt] = true
		for field := range rt.Fields() {
			fieldPath := path + "." + field.Name
			if tag := field.Tag.Get("env"); tag != "" {
				for _, re := range family {
					if re.MatchString(tag) {
						t.Errorf("%s has env tag %q, a provider-family name that must stay bare (exempt table: docs/dev/2026-07-17_env-prefix-migration.md)", fieldPath, tag)
					}
				}
			}
			walk(field.Type, fieldPath)
		}
	}
	walk(reflect.TypeFor[config.Config](), "Config")
}
