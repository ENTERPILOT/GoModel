package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// liteLLMTagsHeader is the header LiteLLM clients send tags in, separated by
// commas.
const liteLLMTagsHeader = "X-Litellm-Tags"

// Clients choose their own tags, so one request contributes at most
// maxLiteLLMTags distinct labels, each at most maxLiteLLMTagLength
// characters. Longer tags are dropped rather than truncated, so a cut tag
// never lands on another label's name.
const (
	maxLiteLLMTags      = 32
	maxLiteLLMTagLength = 128
)

// liteLLMTagEndpoints are the inference endpoints whose JSON body may carry
// LiteLLM tags.
var liteLLMTagEndpoints = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/responses":        true,
	"/v1/messages":         true,
	"/v1/embeddings":       true,
}

// LiteLLMTags turns the tags LiteLLM clients send into request labels, so
// usage and budgets split by them without client changes. It reads the
// x-litellm-tags header, a top-level "tags" body field, and "metadata.tags"
// when it is a list, and removes them before the request goes upstream:
// providers reject them, since "tags" is no OpenAI parameter and OpenAI
// metadata values are strings. A string metadata.tags is valid OpenAI
// metadata and is left alone. The audit entry keeps the body the client
// sent. Tags past the caps are dropped as labels but still stripped.
//
// It runs after authentication, so only authenticated requests are read, and
// before request rewriters and workflow resolution, which see the cleaned
// body.
func LiteLLMTags(auditLogger auditlog.LoggerInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if !core.DescribeEndpoint(req.Method, req.URL.Path).ModelInteraction {
				return next(c)
			}
			labels := splitTags(strings.Join(req.Header.Values(liteLLMTagsHeader), ","))
			if req.Method == http.MethodPost && liteLLMTagEndpoints[req.URL.Path] {
				body, err := requestBodyBytes(c)
				if err != nil {
					return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
				}
				if cleaned, bodyLabels, changed := takeBodyTags(body); changed {
					pinOriginalAuditRequestBody(c, auditLogger)
					applyRewrittenBody(c, cleaned)
					labels = append(labels, bodyLabels...)
				}
			}

			labels = capTags(labels)
			ctx := c.Request().Context()
			changed := false
			if len(labels) > 0 {
				ctx = core.WithRequestLabels(ctx, core.MergeLabels(core.RequestLabelsFromContext(ctx), labels))
				changed = true
			}
			if len(req.Header.Values(liteLLMTagsHeader)) > 0 {
				ctx = core.WithTaggingStripHeaders(ctx, withStripHeader(core.TaggingStripHeadersFromContext(ctx), liteLLMTagsHeader))
				changed = true
			}
			if changed {
				c.SetRequest(c.Request().WithContext(ctx))
			}
			return next(c)
		}
	}
}

// takeBodyTags removes LiteLLM's tag fields from a JSON object body and
// returns their values. changed is false, and body is returned as is, when
// the body carries none or is not a JSON object.
func takeBodyTags(body []byte) (cleaned []byte, labels []string, changed bool) {
	// A cheap scan first: most bodies carry no tags and are never decoded.
	if !gjson.GetBytes(body, "tags").Exists() && !gjson.GetBytes(body, "metadata.tags").Exists() {
		return body, nil, false
	}
	// Values are read from the same decoded maps they are removed from, so a
	// repeated key is judged by the value that is actually kept.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return body, nil, false
	}
	if top, ok := fields["tags"]; ok {
		labels = append(labels, tagValues(gjson.ParseBytes(top))...)
		delete(fields, "tags")
		changed = true
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(fields["metadata"], &metadata) == nil {
		if tags := gjson.ParseBytes(metadata["tags"]); tags.IsArray() {
			labels = append(labels, tagValues(tags)...)
			delete(metadata, "tags")
			changed = true
			if len(metadata) == 0 {
				delete(fields, "metadata")
			} else {
				encoded, err := encodeJSON(metadata)
				if err != nil {
					return body, nil, false
				}
				fields["metadata"] = encoded
			}
		}
	}
	if !changed {
		return body, nil, false
	}
	cleaned, err := encodeJSON(fields)
	if err != nil {
		return body, nil, false
	}
	return cleaned, labels, true
}

// tagValues reads a list of tags, each one label as in LiteLLM, or one
// comma-separated string.
func tagValues(value gjson.Result) []string {
	if value.Type == gjson.String {
		return splitTags(value.Str)
	}
	var out []string
	for _, item := range value.Array() {
		if tag := strings.TrimSpace(item.Str); item.Type == gjson.String && tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// capTags keeps the first maxLiteLLMTags distinct tags of at most
// maxLiteLLMTagLength characters.
func capTags(tags []string) []string {
	var out []string
	for _, tag := range core.MergeLabels(tags) {
		if len(out) == maxLiteLLMTags {
			break
		}
		if utf8.RuneCountInString(tag) <= maxLiteLLMTagLength {
			out = append(out, tag)
		}
	}
	return out
}

func splitTags(value string) []string {
	var out []string
	for tag := range strings.SplitSeq(value, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// encodeJSON marshals without escaping <, >, and &, so string values reach
// the provider byte for byte.
func encodeJSON(value any) (json.RawMessage, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// withStripHeader returns a copy of strip that also names header; the
// context's set is shared and read-only.
func withStripHeader(strip map[string]struct{}, header string) map[string]struct{} {
	out := make(map[string]struct{}, len(strip)+1)
	for name := range strip {
		out[name] = struct{}{}
	}
	out[http.CanonicalHeaderKey(header)] = struct{}{}
	return out
}
