package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// DefaultTaggingDelimiter separates multiple labels inside one header value.
const DefaultTaggingDelimiter = ","

// TaggingConfig declares request labelling based on HTTP headers. Headers
// listed here are read on every request; their values become request labels.
// Declarative entries override admin-store rows with the same header name and
// are read-only in the dashboard.
type TaggingConfig struct {
	Headers []TaggingHeaderConfig `yaml:"headers"`
}

// TaggingHeaderConfig declares one header to extract labels from.
type TaggingHeaderConfig struct {
	// Header is the HTTP header name to read labels from.
	Header string `yaml:"header" json:"header"`

	// Prefix is optionally trimmed from the front of each label. Trimming only
	// affects the extracted label, never the forwarded header value.
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// DoNotPass strips the header before forwarding the request upstream.
	// Default: false (headers are passed through as-is).
	DoNotPass bool `yaml:"do_not_pass,omitempty" json:"do_not_pass,omitempty"`

	// Delimiter splits one header value into multiple labels. Default: ",".
	Delimiter string `yaml:"delimiter,omitempty" json:"delimiter,omitempty"`
}

var taggingHeaderEnvRegex = regexp.MustCompile(`^TAGGING_HEADER_([0-9]+)=`)

// applyTaggingEnv reads TAGGING_HEADER_<N> env vars (with optional
// TAGGING_HEADER_<N>_PREFIX, TAGGING_HEADER_<N>_DONOTPASS, and
// TAGGING_HEADER_<N>_DELIMITER companions) and merges them over the
// YAML-declared list. Env entries override YAML entries with the same header
// name, consistent with the rest of the config pipeline where env always wins.
func applyTaggingEnv(cfg *Config) error {
	indexes := make([]int, 0)
	for _, kv := range os.Environ() {
		m := taggingHeaderEnvRegex.FindStringSubmatch(kv)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		indexes = append(indexes, n)
	}
	sort.Ints(indexes)

	fromEnv := make([]TaggingHeaderConfig, 0, len(indexes))
	for _, n := range indexes {
		key := fmt.Sprintf("TAGGING_HEADER_%d", n)
		header := strings.TrimSpace(os.Getenv(key))
		if header == "" {
			continue
		}
		fromEnv = append(fromEnv, TaggingHeaderConfig{
			Header:    header,
			Prefix:    os.Getenv(key + "_PREFIX"),
			DoNotPass: parseBool(os.Getenv(key + "_DONOTPASS")),
			Delimiter: os.Getenv(key + "_DELIMITER"),
		})
	}

	cfg.Tagging.Headers = mergeByKey(cfg.Tagging.Headers, fromEnv, func(header TaggingHeaderConfig) string {
		return canonicalTextKey(header.Header)
	})
	return nil
}

// normalizeTaggingConfig canonicalizes header names, applies the default
// delimiter, and rejects invalid or duplicate entries.
func normalizeTaggingConfig(cfg *TaggingConfig) error {
	seen := make(map[string]struct{}, len(cfg.Headers))
	for i := range cfg.Headers {
		h := &cfg.Headers[i]
		name, err := NormalizeHeaderName(h.Header, "")
		if err != nil {
			return fmt.Errorf("tagging.headers[%d]: %w", i, err)
		}
		// Credential-bearing headers must never be tagging label sources:
		// their values would be persisted as plaintext labels in usage and
		// audit records, bypassing audit header redaction.
		if core.IsCredentialHeader(name) {
			return fmt.Errorf("tagging.headers[%d]: header %q may carry credentials and cannot be used for tagging", i, name)
		}
		h.Header = name
		if _, dup := seen[name]; dup {
			return fmt.Errorf("tagging.headers: duplicate header %q", name)
		}
		seen[name] = struct{}{}
		if h.Delimiter == "" {
			h.Delimiter = DefaultTaggingDelimiter
		}
	}
	return nil
}
