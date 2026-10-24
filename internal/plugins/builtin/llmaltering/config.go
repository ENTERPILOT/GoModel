// Package llmaltering is the built-in llm_based_altering plugin: it rewrites
// the text of selected message roles through an auxiliary model, both in the
// prompt phase (before the provider call) and in the response phase.
package llmaltering

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultMaxTokens caps the auxiliary rewrite completion when unset.
const DefaultMaxTokens = 4096

// Config is the instance configuration.
type Config struct {
	Model             string   `json:"model"`
	Provider          string   `json:"provider,omitempty"`
	Prompt            string   `json:"prompt,omitempty"`
	Roles             []string `json:"roles,omitempty"`
	SkipContentPrefix string   `json:"skip_content_prefix,omitempty"`
	MaxTokens         int      `json:"max_tokens,omitempty"`
}

// ParseConfig decodes and normalizes a config: the provider hint is folded
// into the model ("provider/model"), roles are lowercased and deduplicated
// (default ["user"]), and an empty prompt selects DefaultPrompt.
func ParseConfig(raw json.RawMessage) (Config, error) {
	var cfg Config
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("invalid llm_based_altering config: %w", err)
		}
	}
	model, err := QualifiedModel(cfg.Model, cfg.Provider)
	if err != nil {
		return Config{}, err
	}
	cfg.Model = model
	cfg.Provider = ""
	cfg.Prompt = ResolvePrompt(cfg.Prompt)
	cfg.SkipContentPrefix = strings.TrimSpace(cfg.SkipContentPrefix)
	cfg.MaxTokens = EffectiveMaxTokens(cfg.MaxTokens)
	roles, err := NormalizeRoles(cfg.Roles)
	if err != nil {
		return Config{}, err
	}
	cfg.Roles = roles
	return cfg, nil
}

// QualifiedModel folds an optional provider hint into the model selector.
func QualifiedModel(model, provider string) (string, error) {
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)
	if model == "" {
		return "", fmt.Errorf("llm_based_altering model is required")
	}
	if provider == "" {
		return model, nil
	}
	if prefix, _, ok := strings.Cut(model, "/"); ok {
		if prefix != provider {
			return "", fmt.Errorf("invalid llm_based_altering model selector: provider %q conflicts with model %q", provider, model)
		}
		return model, nil
	}
	return provider + "/" + model, nil
}

// ResolvePrompt returns the effective system prompt.
func ResolvePrompt(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return DefaultPrompt
	}
	return strings.TrimSpace(prompt)
}

// EffectiveMaxTokens returns the effective max_tokens value.
func EffectiveMaxTokens(maxTokens int) int {
	if maxTokens <= 0 {
		return DefaultMaxTokens
	}
	return maxTokens
}

// NormalizeRoles validates, lowercases, and deduplicates the target roles.
func NormalizeRoles(roles []string) ([]string, error) {
	if len(roles) == 0 {
		return []string{"user"}, nil
	}
	seen := make(map[string]struct{}, len(roles))
	normalized := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.ToLower(strings.TrimSpace(role))
		switch role {
		case "system", "user", "assistant", "tool":
		case "":
			continue
		default:
			return nil, fmt.Errorf("invalid llm_based_altering role: %q (must be system, user, assistant, or tool)", role)
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		normalized = append(normalized, role)
	}
	if len(normalized) == 0 {
		return []string{"user"}, nil
	}
	return normalized, nil
}
