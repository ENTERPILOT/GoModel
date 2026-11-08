package auditlog

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ForPath returns the capture config for one request path. It turns body
// capture off for embeddings endpoints when LogEmbeddingBodies is disabled,
// so those entries keep their metadata but no request or response body.
func (c Config) ForPath(path string) Config {
	if c.LogBodies && !c.LogEmbeddingBodies && isEmbeddingsPath(path) {
		c.LogBodies = false
	}
	return c
}

// isEmbeddingsPath reports whether path is an embeddings endpoint: the
// OpenAI-compatible /v1/embeddings route or a provider passthrough call whose
// last path segment names an embeddings operation (OpenAI-style "embeddings",
// Cohere/Ollama "embed", Gemini ":embedContent" and ":batchEmbedContents").
func isEmbeddingsPath(path string) bool {
	if _, endpoint, ok := core.ParseProviderPassthroughPath(path); ok {
		endpoint = strings.Trim(endpoint, "/")
		last := endpoint[strings.LastIndex(endpoint, "/")+1:]
		return last == "embeddings" || last == "embed" ||
			strings.HasSuffix(last, ":embedContent") || strings.HasSuffix(last, ":batchEmbedContents")
	}
	return core.DescribeEndpointPath(path).Operation == core.OperationEmbeddings
}
