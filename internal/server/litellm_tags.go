package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// liteLLMTagsHeader is the header LiteLLM clients send tags in, separated by
// commas.
const liteLLMTagsHeader = "X-Litellm-Tags"

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
// sent.
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
	top := gjson.GetBytes(body, "tags")
	meta := gjson.GetBytes(body, "metadata.tags")
	if !top.Exists() && !meta.IsArray() {
		return body, nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return body, nil, false
	}
	if top.Exists() {
		labels = append(labels, tagValues(top)...)
		delete(fields, "tags")
	}
	if meta.IsArray() {
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(fields["metadata"], &metadata); err != nil {
			return body, nil, false
		}
		labels = append(labels, tagValues(meta)...)
		delete(metadata, "tags")
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
	cleaned, err := encodeJSON(fields)
	if err != nil {
		return body, nil, false
	}
	return cleaned, labels, true
}

// tagValues reads a list of tags, or one comma-separated string.
func tagValues(value gjson.Result) []string {
	if value.Type == gjson.String {
		return splitTags(value.Str)
	}
	var out []string
	for _, item := range value.Array() {
		if item.Type == gjson.String {
			out = append(out, splitTags(item.Str)...)
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
