package modeldata

import (
	"testing"
)

func TestInferModesFromID(t *testing.T) {
	tests := []struct {
		id   string
		want string // single expected mode, or "" for no inference
	}{
		// "embed" substring — the common local-model spellings.
		{"nomic-embed-text", "embedding"},
		{"nomic-embed-text-v1.5.Q8_0.gguf", "embedding"},
		{"text-embedding-nomic-embed-text-v1.5@q8_0", "embedding"},
		{"mxbai-embed-large", "embedding"},
		{"snowflake-arctic-embed", "embedding"},
		{"embeddinggemma", "embedding"},
		{"qwen3-embedding-0.6b", "embedding"},
		{"text-embedding-3-small", "embedding"},
		{"granite-embedding:278m", "embedding"},
		// Family tokens without "embed" in the name.
		{"bge-m3", "embedding"},
		{"bge-large-en-v1.5", "embedding"},
		{"e5-large-v2", "embedding"},
		{"gte-large", "embedding"},
		{"all-minilm", "embedding"},
		{"all-MiniLM-L6-v2", "embedding"},
		// Namespaced IDs classify by the final path segment.
		{"BAAI/bge-m3", "embedding"},
		{"intfloat/e5-mistral-7b-instruct", "embedding"},
		// Rerankers.
		{"bge-reranker-v2-m3", "rerank"},
		{"jina-reranker-v2", "rerank"},
		// Token matching must not fire on lookalike substrings.
		{"gemma-3n-e4b", ""},
		{"bge2000-chat", ""},
		{"gte", "embedding"}, // bare family name still counts
		// Ordinary chat models stay uninferred.
		{"gpt-4o", ""},
		{"llama-3.1-8b-instruct", ""},
		{"qwen2.5-coder:7b", ""},
		{"", ""},
		{"  ", ""},
		{"org/", ""},
	}
	for _, tt := range tests {
		got := InferModesFromID(tt.id)
		switch {
		case tt.want == "" && len(got) != 0:
			t.Errorf("InferModesFromID(%q) = %v, want none", tt.id, got)
		case tt.want != "" && (len(got) != 1 || got[0] != tt.want):
			t.Errorf("InferModesFromID(%q) = %v, want [%s]", tt.id, got, tt.want)
		}
	}
}
