// Package litellmmigrate converts a LiteLLM proxy config.yaml into an
// equivalent GoModel config.yaml, the environment variables it needs, and a
// report of everything that was changed, approximated, or left behind.
package litellmmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// liteLLMConfig is the subset of the LiteLLM proxy config.yaml the converter
// understands. Settings sections stay loosely typed so every key the converter
// does not handle can be named in the report instead of silently dropped.
type liteLLMConfig struct {
	ModelList            []deployment      `yaml:"model_list"`
	CredentialList       []credential      `yaml:"credential_list"`
	RouterSettings       map[string]any    `yaml:"router_settings"`
	LiteLLMSettings      map[string]any    `yaml:"litellm_settings"`
	GeneralSettings      map[string]any    `yaml:"general_settings"`
	EnvironmentVariables map[string]string `yaml:"environment_variables"`
	Include              []string          `yaml:"include"`
	Extra                map[string]any    `yaml:",inline"`
}

// deployment is one model_list entry: a public model_name served by one
// upstream deployment. Several entries sharing a model_name form a load
// balanced group.
type deployment struct {
	ModelName     string         `yaml:"model_name"`
	LiteLLMParams litellmParams  `yaml:"litellm_params"`
	ModelInfo     modelInfo      `yaml:"model_info"`
	Extra         map[string]any `yaml:",inline"`
}

type litellmParams struct {
	Model                       string         `yaml:"model"`
	APIKey                      string         `yaml:"api_key"`
	APIBase                     string         `yaml:"api_base"`
	APIVersion                  string         `yaml:"api_version"`
	CustomLLMProvider           string         `yaml:"custom_llm_provider"`
	CredentialName              string         `yaml:"litellm_credential_name"`
	RPM                         *float64       `yaml:"rpm"`
	TPM                         *float64       `yaml:"tpm"`
	Weight                      *float64       `yaml:"weight"`
	AWSRegionName               string         `yaml:"aws_region_name"`
	AWSAccessKeyID              string         `yaml:"aws_access_key_id"`
	AWSSecretAccessKey          string         `yaml:"aws_secret_access_key"`
	AWSProfileName              string         `yaml:"aws_profile_name"`
	VertexProject               string         `yaml:"vertex_project"`
	VertexLocation              string         `yaml:"vertex_location"`
	VertexCredentials           any            `yaml:"vertex_credentials"`
	InputCostPerToken           *float64       `yaml:"input_cost_per_token"`
	OutputCostPerToken          *float64       `yaml:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64       `yaml:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64       `yaml:"cache_creation_input_token_cost"`
	Extra                       map[string]any `yaml:",inline"`
}

type modelInfo struct {
	InputCostPerToken           *float64 `yaml:"input_cost_per_token"`
	OutputCostPerToken          *float64 `yaml:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `yaml:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `yaml:"cache_creation_input_token_cost"`
	MaxInputTokens              *int     `yaml:"max_input_tokens"`
	MaxOutputTokens             *int     `yaml:"max_output_tokens"`
	// AccessGroups restricts the model to keys and teams in these groups.
	AccessGroups []string `yaml:"access_groups"`
	// Other model_info keys (id, mode, base_model, ...) are descriptive
	// metadata; they are accepted and ignored.
	Extra map[string]any `yaml:",inline"`
}

// credential is a credential_list entry that deployments reference by
// litellm_credential_name.
type credential struct {
	Name   string            `yaml:"credential_name"`
	Values map[string]string `yaml:"credential_values"`
}

// envRefPrefix marks a LiteLLM value read from the environment.
const envRefPrefix = "os.environ/"

// maxIncludeDepth bounds nested include: directives.
const maxIncludeDepth = 8

// loadFile reads a LiteLLM config file and resolves its include: directives
// relative to the file's directory, the way the LiteLLM proxy does.
func loadFile(path string, depth int) (*liteLLMConfig, error) {
	if depth > maxIncludeDepth {
		return nil, fmt.Errorf("%s: include nesting deeper than %d", path, maxIncludeDepth)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, include := range cfg.Include {
		includePath := include
		if !filepath.IsAbs(includePath) {
			includePath = filepath.Join(filepath.Dir(path), includePath)
		}
		included, err := loadFile(includePath, depth+1)
		if err != nil {
			return nil, fmt.Errorf("include %q: %w", include, err)
		}
		cfg.merge(included)
	}
	cfg.Include = nil
	return cfg, nil
}

func parse(data []byte) (*liteLLMConfig, error) {
	var cfg liteLLMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse LiteLLM config: %w", err)
	}
	return &cfg, nil
}

// merge folds an included file into cfg: lists are appended, maps are merged
// with cfg's own keys winning.
func (cfg *liteLLMConfig) merge(other *liteLLMConfig) {
	cfg.ModelList = append(cfg.ModelList, other.ModelList...)
	cfg.CredentialList = append(cfg.CredentialList, other.CredentialList...)
	cfg.RouterSettings = mergeMaps(cfg.RouterSettings, other.RouterSettings)
	cfg.LiteLLMSettings = mergeMaps(cfg.LiteLLMSettings, other.LiteLLMSettings)
	cfg.GeneralSettings = mergeMaps(cfg.GeneralSettings, other.GeneralSettings)
	cfg.Extra = mergeMaps(cfg.Extra, other.Extra)
	for key, value := range other.EnvironmentVariables {
		if _, exists := cfg.EnvironmentVariables[key]; !exists {
			if cfg.EnvironmentVariables == nil {
				cfg.EnvironmentVariables = map[string]string{}
			}
			cfg.EnvironmentVariables[key] = value
		}
	}
}

func mergeMaps[V any](base, other map[string]V) map[string]V {
	for key, value := range other {
		if _, exists := base[key]; exists {
			continue
		}
		if base == nil {
			base = map[string]V{}
		}
		base[key] = value
	}
	return base
}

// envRef returns the variable name of an os.environ/NAME reference.
func envRef(value string) (string, bool) {
	name, ok := strings.CutPrefix(strings.TrimSpace(value), envRefPrefix)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// collectEnvRefs returns the names of all os.environ/ references in cfg, in
// first-seen order.
func collectEnvRefs(cfg *liteLLMConfig) []string {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil
	}
	var tree any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return nil
	}
	var names []string
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case string:
			if name, ok := envRef(v); ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(v[key])
			}
		}
	}
	walk(tree)
	return names
}
