package modeldata

import (
	"sort"
	"strings"
)

// ProviderModelIDs returns the enabled model IDs the list records for a
// provider type, as the provider's own model listing would report them, in
// sorted order.
func (l *ModelList) ProviderModelIDs(providerType string) []string {
	if l == nil {
		return nil
	}
	prefix := strings.TrimSpace(providerType) + "/"
	seen := make(map[string]struct{})
	var ids []string
	for key, entry := range l.ProviderModels {
		modelID, ok := strings.CutPrefix(key, prefix)
		if !ok || !entry.Enabled {
			continue
		}
		if actual, ok := entry.actualModelID(); ok {
			modelID = actual
		}
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if _, dup := seen[modelID]; dup {
			continue
		}
		seen[modelID] = struct{}{}
		ids = append(ids, modelID)
	}
	sort.Strings(ids)
	return ids
}

// ProviderDefaultBaseURL returns the list's default API base URL for a
// provider type, or "" when it records none.
func (l *ModelList) ProviderDefaultBaseURL(providerType string) string {
	if l == nil {
		return ""
	}
	provider, ok := l.Providers[strings.TrimSpace(providerType)]
	if !ok || provider.DefaultBaseURL == nil {
		return ""
	}
	return strings.TrimSpace(*provider.DefaultBaseURL)
}
