package users

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
)

// NormalizeAllowedModels validates and canonicalizes a model allowlist. It
// accepts the dashboard-friendly wildcard forms "*" (every model) and
// "provider/*" (every model of one provider) and stores them as the canonical
// "/" and "provider/" selectors. Duplicates collapse; order is preserved.
func NormalizeAllowedModels(catalog Catalog, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var providerNames []string
	if catalog != nil {
		providerNames = catalog.ProviderNames()
	}
	known := make(map[string]struct{}, len(providerNames))
	for _, name := range providerNames {
		known[strings.TrimSpace(name)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" {
			entry = "/"
		} else if strings.HasSuffix(entry, "/*") {
			entry = strings.TrimSuffix(entry, "*")
		}
		selector, err := modelselectors.NormalizeInputWithProviderNames(providerNames, entry)
		if err != nil {
			return nil, newValidationError("invalid allowed_models entry "+entry+": "+err.Error(), err)
		}
		// A slash-shaped entry whose prefix is not a configured provider would
		// be stored as a model-wide selector but read back as provider/model,
		// so it can never match. Reject it while the author can still fix it.
		if catalog != nil && selector.ProviderName == "" && !modelselectors.IsGlobal(selector.Selector) {
			if prefix, _, ok := strings.Cut(selector.Selector, "/"); ok {
				if _, exists := known[prefix]; !exists {
					return nil, newValidationError("invalid allowed_models entry "+entry+": unknown provider "+prefix, nil)
				}
			}
		}
		if _, dup := seen[selector.Selector]; dup {
			continue
		}
		seen[selector.Selector] = struct{}{}
		result = append(result, selector.Selector)
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// Matches reports whether selector satisfies at least one canonical allowlist
// entry. An empty allowlist matches everything.
func Matches(allowed []string, selector core.ModelSelector) bool {
	if len(allowed) == 0 {
		return true
	}
	provider := strings.TrimSpace(selector.Provider)
	model := strings.TrimSpace(selector.Model)
	for _, entry := range allowed {
		if matchesEntry(entry, provider, model) {
			return true
		}
	}
	return false
}

// Covers reports whether every model that the canonical entry permits is
// also permitted by allowed, so a credential limited to allowed may grant
// entry to another key. An empty allowlist covers everything.
func Covers(allowed []string, entry string) bool {
	if len(allowed) == 0 {
		return true
	}
	entry = strings.TrimSpace(entry)
	for _, candidate := range allowed {
		candidate = strings.TrimSpace(candidate)
		switch {
		case candidate == "":
			continue
		case modelselectors.IsGlobal(candidate), candidate == entry:
			return true
		}
	}
	switch {
	case entry == "", modelselectors.IsGlobal(entry), strings.HasSuffix(entry, "/"):
		// Only the global selector or the same provider-wide one covers these.
		return false
	}
	entryProvider, entryModel := modelselectors.ParseStoredParts(entry)
	if entryProvider == "" {
		// A bare name permits the model on every provider; only the same bare
		// name or the global selector covers it.
		return false
	}
	return Matches(allowed, core.ModelSelector{Provider: entryProvider, Model: entryModel})
}

// matchesEntry interprets one canonical selector: "/" is global, a trailing
// slash is provider-wide, a "provider/model" pair is exact, and a bare name is
// model-wide across providers.
func matchesEntry(entry, provider, model string) bool {
	entry = strings.TrimSpace(entry)
	switch {
	case entry == "":
		return false
	case modelselectors.IsGlobal(entry):
		return true
	case strings.HasSuffix(entry, "/"):
		return provider != "" && strings.TrimSuffix(entry, "/") == provider
	}
	entryProvider, entryModel := modelselectors.ParseStoredParts(entry)
	if entryProvider == "" {
		return entryModel == model
	}
	return entryProvider == provider && entryModel == model
}
