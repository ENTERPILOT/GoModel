// Package tagging labels requests based on configurable HTTP headers. Each
// rule names one header whose value carries request labels; labels flow into
// usage tracking and audit logs, and rules can mark their header as
// do-not-pass so it is never forwarded to upstream providers.
package tagging

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// ValidationError marks rule failures caused by caller input, so API handlers
// can report them as a bad request instead of a storage failure.
type ValidationError struct{ err error }

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

func newValidationError(format string, args ...any) error {
	return &ValidationError{err: fmt.Errorf(format, args...)}
}

// IsValidationError reports whether err stems from invalid caller input.
func IsValidationError(err error) bool {
	var validationErr *ValidationError
	return errors.As(err, &validationErr)
}

// DefaultDelimiter separates multiple labels inside one header value.
const DefaultDelimiter = ","

// Rule configures label extraction from one request header. Managed rules
// come from config.yaml / TAGGING_HEADER_* env vars, override store rows with
// the same header name, and are read-only in the dashboard.
type Rule struct {
	// Header is the canonical HTTP header name to read labels from.
	Header string `json:"header" bson:"header"`

	// Prefix is optionally trimmed from the front of each label. Trimming only
	// affects the extracted label, never the forwarded header value.
	Prefix string `json:"prefix,omitempty" bson:"prefix,omitempty"`

	// DoNotPass strips the header before forwarding the request upstream.
	// Default: false (headers are passed through as-is).
	DoNotPass bool `json:"do_not_pass,omitempty" bson:"do_not_pass,omitempty"`

	// Delimiter splits one header value into multiple labels. Default: ",".
	Delimiter string `json:"delimiter,omitempty" bson:"delimiter,omitempty"`

	// Managed marks a rule declared in config/env; such rules are read-only in
	// the dashboard. Never persisted.
	Managed bool `json:"managed,omitempty" bson:"-"`
}

// NormalizeRules canonicalizes header names, applies the default delimiter,
// and rejects invalid, credential-bearing, or duplicate entries in place.
// Rejections are ValidationErrors.
func NormalizeRules(rules []Rule) error {
	seen := make(map[string]struct{}, len(rules))
	for i := range rules {
		rule := &rules[i]
		name, err := config.NormalizeHeaderName(rule.Header, "")
		if err != nil {
			return newValidationError("tagging rule %d: %v", i, err)
		}
		if core.IsCredentialHeader(name) {
			return newValidationError("tagging rule %d: header %q may carry credentials and cannot be used for tagging", i, name)
		}
		rule.Header = name
		if _, dup := seen[name]; dup {
			return newValidationError("tagging rules: duplicate header %q", name)
		}
		seen[name] = struct{}{}
		if rule.Delimiter == "" {
			rule.Delimiter = DefaultDelimiter
		}
	}
	return nil
}

// ExtractLabels reads every rule's header and returns the deduplicated labels
// in rule order. Each header value is split by the rule's delimiter and each
// piece is whitespace-trimmed; when the rule has a prefix, it is trimmed from
// pieces that carry it, and pieces without it are kept as-is.
func ExtractLabels(rules []Rule, headers http.Header) []string {
	if len(rules) == 0 || len(headers) == 0 {
		return nil
	}
	var labels []string
	seen := make(map[string]struct{})
	for _, rule := range rules {
		delimiter := rule.Delimiter
		if delimiter == "" {
			delimiter = DefaultDelimiter
		}
		for _, value := range headers.Values(rule.Header) {
			for piece := range strings.SplitSeq(value, delimiter) {
				label := strings.TrimSpace(piece)
				if rule.Prefix != "" {
					label = strings.TrimSpace(strings.TrimPrefix(label, rule.Prefix))
				}
				if label == "" {
					continue
				}
				if _, dup := seen[label]; dup {
					continue
				}
				seen[label] = struct{}{}
				labels = append(labels, label)
			}
		}
	}
	return labels
}

// StripHeaderSet returns the canonical header names marked do-not-pass.
func StripHeaderSet(rules []Rule) map[string]struct{} {
	var strip map[string]struct{}
	for _, rule := range rules {
		if !rule.DoNotPass {
			continue
		}
		if strip == nil {
			strip = make(map[string]struct{})
		}
		strip[http.CanonicalHeaderKey(rule.Header)] = struct{}{}
	}
	return strip
}
