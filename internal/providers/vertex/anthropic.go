package vertex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/providers"
)

// vertexAnthropicVersion is the anthropic_version Vertex requires in the body
// of Claude requests, in place of Anthropic's anthropic-version header.
const vertexAnthropicVersion = "vertex-2023-10-16"

func useAnthropicAPI(apiMode string) bool {
	return strings.EqualFold(strings.TrimSpace(apiMode), apiModeAnthropic)
}

// vertexAnthropicBaseURL returns the Anthropic publisher base URL for the
// configured base URL or project/location pair. A base URL that is neither an
// /endpoints/openapi nor a /publishers/google URL is used as-is.
func vertexAnthropicBaseURL(providerCfg providers.ProviderConfig) string {
	native := vertexNativeBaseURL(providerCfg)
	if root, ok := strings.CutSuffix(native, "/publishers/google"); ok {
		return root + "/publishers/anthropic"
	}
	return native
}

// anthropicTransport turns the Anthropic adapter's POST {base}/messages calls
// into Vertex's {base}/models/{model}:rawPredict (or :streamRawPredict) calls:
// the model moves from the body into the path, anthropic_version moves from
// the header into the body, and the Anthropic API key header is dropped so
// only the Google bearer token authenticates the request.
type anthropicTransport struct {
	base http.RoundTripper
}

func anthropicHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone := *base
	clone.Transport = &anthropicTransport{base: transport}
	return &clone
}

func (t *anthropicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/messages") || req.Body == nil {
		return t.base.RoundTrip(req)
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("vertex anthropic: decode request body: %w", err)
	}
	var model string
	var stream bool
	if err := json.Unmarshal(body["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("vertex anthropic: request body has no model")
	}
	if s, ok := body["stream"]; ok {
		_ = json.Unmarshal(s, &stream)
	}
	delete(body, "model")
	body["anthropic_version"] = json.RawMessage(`"` + vertexAnthropicVersion + `"`)
	rewritten, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	verb := ":rawPredict"
	if stream {
		verb = ":streamRawPredict"
	}
	out := req.Clone(req.Context())
	u := *req.URL
	u.Path = strings.TrimSuffix(u.Path, "/messages") + "/models/" + strings.TrimPrefix(strings.TrimSpace(model), "anthropic/") + verb
	u.RawPath = ""
	out.URL = &u
	out.Body = io.NopCloser(bytes.NewReader(rewritten))
	out.ContentLength = int64(len(rewritten))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(rewritten)), nil }
	out.Header.Del("x-api-key")
	out.Header.Del("anthropic-version")
	return t.base.RoundTrip(out)
}
