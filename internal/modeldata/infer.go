package modeldata

import "strings"

// embeddingFamilyTokens are model-family names that identify embedding models
// without containing the substring "embed" (bge-m3, e5-large-v2, gte-large,
// all-minilm). Matched as whole delimited tokens only, so IDs like
// "gemma-3n-e4b" or "bge2000-chat" are not misclassified.
var embeddingFamilyTokens = map[string]struct{}{
	"bge":    {},
	"e5":     {},
	"gte":    {},
	"minilm": {},
}

// InferModesFromID guesses a model's modes from its ID alone. It is a
// last-resort fallback for models absent from the remote model registry —
// typically local models served by llama.cpp, LM Studio, Ollama, or vLLM,
// whose IDs (often GGUF file names or user-chosen aliases) the registry can
// never enumerate. Without a mode, such a model is never categorized as an
// embedding model anywhere in the gateway even though calling it works fine.
//
// The heuristic is deliberately conservative: it only claims the modes it is
// confident about and returns nil otherwise, so unknown models keep the
// "no metadata" state rather than being mislabeled. Real registry entries and
// operator-declared metadata always take precedence over this inference.
func InferModesFromID(modelID string) []string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	// Namespaced IDs (hf repo paths, "org/model") classify by the final segment.
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
	}
	if id == "" {
		return nil
	}
	if strings.Contains(id, "rerank") {
		return []string{"rerank"}
	}
	if strings.Contains(id, "embed") {
		return []string{"embedding"}
	}
	for _, token := range strings.FieldsFunc(id, isModelIDDelimiter) {
		if _, ok := embeddingFamilyTokens[token]; ok {
			return []string{"embedding"}
		}
	}
	return nil
}

func isModelIDDelimiter(r rune) bool {
	switch r {
	case '-', '_', '.', ':', '@', ' ':
		return true
	}
	return false
}
