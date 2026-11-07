package auditlog

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// forPath returns the capture config for one request path. It turns body
// capture off for embeddings endpoints when LogEmbeddingBodies is disabled,
// so those entries keep their metadata but no request or response body.
func (c Config) forPath(path string) Config {
	if c.LogBodies && !c.LogEmbeddingBodies && isEmbeddingsPath(path) {
		c.LogBodies = false
	}
	return c
}

// isEmbeddingsPath reports whether path is an embeddings endpoint, either the
// OpenAI-compatible /v1/embeddings route or a provider passthrough call
// (/p/{provider}/embeddings or /p/{provider}/v1/embeddings).
func isEmbeddingsPath(path string) bool {
	if _, endpoint, ok := core.ParseProviderPassthroughPath(path); ok {
		endpoint = strings.Trim(endpoint, "/")
		return endpoint == "embeddings" || endpoint == "v1/embeddings"
	}
	return core.DescribeEndpointPath(path).Operation == core.OperationEmbeddings
}
