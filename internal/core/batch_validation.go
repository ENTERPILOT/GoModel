package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// batchItemURLs are the paths a batch item may target, and the canonical
// endpoint of each: the subrequests whose model access and guardrails the
// gateway checks before the provider runs them. Azure OpenAI batch files use
// the unversioned forms.
var batchItemURLs = map[string]string{
	"/v1/chat/completions": "/v1/chat/completions",
	"/chat/completions":    "/v1/chat/completions",
	"/v1/responses":        "/v1/responses",
	"/responses":           "/v1/responses",
	"/v1/embeddings":       "/v1/embeddings",
	"/embeddings":          "/v1/embeddings",
}

// batchItemFields are the keys of a batch item, which a case variant must not
// shadow.
var batchItemFields = []string{"custom_id", "method", "url", "body"}

// batchItemEndpoint returns the canonical endpoint for a batch item URL. The
// URL is accepted generously (a full URL, a query, or a trailing slash), but
// its path must then be one of batchItemURLs exactly, and the item is
// forwarded with that path (BatchItemPath): any other spelling could be
// checked as one endpoint and run upstream as another.
func batchItemEndpoint(url string) (string, bool) {
	endpoint, ok := batchItemURLs[BatchItemPath(url)]
	return endpoint, ok
}

// BatchItemPath is the path a batch item URL is forwarded with.
func BatchItemPath(url string) string {
	return NormalizeOperationPath(url)
}

// ValidateBatchEndpoint checks a batch's default endpoint, which items without
// their own url target.
func ValidateBatchEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	if _, ok := batchItemEndpoint(endpoint); !ok {
		return NewInvalidRequestError(unsupportedBatchURLMessage("endpoint", endpoint), nil)
	}
	return nil
}

// validateBatchItem checks that a batch item is a request the gateway can
// check before the provider runs it, and that its JSON reads the same to
// every parser: no key is repeated, and none differs from another, or from a
// batch item field, only in case.
func validateBatchItem(defaultEndpoint string, item BatchRequestItem) error {
	if method := strings.TrimSpace(item.Method); method != "" && !strings.EqualFold(method, http.MethodPost) {
		return fmt.Errorf("method %s is not supported; batch items must use POST", method)
	}
	url := ResolveBatchItemEndpoint(defaultEndpoint, item.URL)
	if url == "" {
		return errors.New("url is required")
	}
	if _, ok := batchItemEndpoint(url); !ok {
		return errors.New(unsupportedBatchURLMessage("url", url))
	}
	if len(strings.TrimSpace(string(item.Body))) == 0 {
		return errors.New("body is required")
	}
	var shadowed string
	item.ExtraFields.forEachMember(func(name string, _ gjson.Result) bool {
		for _, field := range batchItemFields {
			if strings.EqualFold(name, field) {
				shadowed = name
				return false
			}
		}
		return true
	})
	if shadowed != "" {
		return fmt.Errorf("key %q is not a batch item field; use lowercase keys", shadowed)
	}
	return checkBatchJSONKeys(item.Body)
}

// checkBatchJSONKeys rejects a JSON object that repeats a key at any depth, or
// whose top-level keys differ only in case. Parsers disagree on which of two
// such keys wins, so the body could be checked as one request and run
// upstream as another. Nested keys may differ in case, as a tool schema's
// properties legitimately can.
func checkBatchJSONKeys(data []byte) error {
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return errors.New("expected a JSON object")
	}
	if key, ok := repeatedJSONKey(root); ok {
		return fmt.Errorf("key %q is repeated", key)
	}
	seen := map[string]string{}
	var first, second string
	root.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		folded := strings.ToLower(name)
		if prior, ok := seen[folded]; ok {
			first, second = prior, name
			return false
		}
		seen[folded] = name
		return true
	})
	if first != "" {
		return fmt.Errorf("keys %q and %q differ only in case", first, second)
	}
	return nil
}

// repeatedJSONKey returns the first key an object in value repeats, at any
// depth. Keys are compared unescaped, so "model" repeats "model".
func repeatedJSONKey(value gjson.Result) (string, bool) {
	var repeated string
	switch {
	case value.IsObject():
		seen := map[string]struct{}{}
		value.ForEach(func(key, member gjson.Result) bool {
			name := key.String()
			if _, ok := seen[name]; ok {
				repeated = name
				return false
			}
			seen[name] = struct{}{}
			if nested, ok := repeatedJSONKey(member); ok {
				repeated = nested
				return false
			}
			return true
		})
	case value.IsArray():
		value.ForEach(func(_, element gjson.Result) bool {
			if nested, ok := repeatedJSONKey(element); ok {
				repeated = nested
				return false
			}
			return true
		})
	}
	return repeated, repeated != ""
}

func unsupportedBatchURLMessage(field, url string) string {
	return fmt.Sprintf("unsupported batch %s %q; use /v1/chat/completions, /v1/responses, or /v1/embeddings", field, url)
}
