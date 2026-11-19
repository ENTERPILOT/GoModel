package providers

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/goccy/go-json"
	"golang.org/x/net/http/httpguts"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

const (
	// maxProviderHeaders and maxProviderHeaderBytes bound one provider's
	// `headers`: every request to the provider carries them, and upstreams
	// commonly refuse a header block past 8 KiB.
	maxProviderHeaders     = 32
	maxProviderHeaderBytes = 8 << 10
)

// parseHeadersEnv decodes a `<PROVIDER>_HEADERS` variable: a JSON object of
// header names to string values. The error never repeats the value, which may
// hold a secret. The result is non-nil, so an empty object still replaces the
// headers config.yaml declares.
func parseHeadersEnv(value string) (map[string]string, error) {
	headers := map[string]string{}
	if err := json.Unmarshal([]byte(value), &headers); err != nil || headers == nil {
		return nil, fmt.Errorf("value is not a JSON object of header names to string values")
	}
	return headers, nil
}

// rejectUnsetHeaderPlaceholders fails on a header value still holding a
// legacy ${VAR} that environment expansion left in place because VAR is
// unset. Unlike an API key, a header cannot be dropped quietly: the provider
// would start without the token its proxy wants, or send the placeholder as
// the token. It runs before secret resolution, the only point where "${" is
// still a placeholder and not data.
func rejectUnsetHeaderPlaceholders(candidates map[string]config.RawProviderConfig) error {
	for _, name := range slices.Sorted(maps.Keys(candidates)) {
		headers := candidates[name].Headers
		for _, header := range slices.Sorted(maps.Keys(headers)) {
			if config.HasUnresolvedPlaceholder(headers[header]) {
				return fmt.Errorf("%s: value references an environment variable that is not set", providerHeaderPath(name, header))
			}
		}
	}
	return nil
}

// validateProviderHeaders checks the resolved `headers` of every provider
// against the rules of its type (see validateHeaders), naming the provider.
func validateProviderHeaders(providers map[string]ProviderConfig, discovery map[string]DiscoveryConfig) error {
	for _, name := range slices.Sorted(maps.Keys(providers)) {
		cfg := providers[name]
		if err := validateHeaders(cfg.Type, discovery[cfg.Type], cfg.Headers); err != nil {
			return fmt.Errorf("providers.%s.%w", name, err)
		}
	}
	return nil
}

// ValidateHeaders checks static headers for a provider of the given type
// against the rules of that type. See validateHeaders.
func (f *ProviderFactory) ValidateHeaders(providerType string, headers map[string]string) error {
	return validateHeaders(providerType, f.discoveryConfig(providerType), headers)
}

// validateHeaders checks a provider's static headers. Because they are sent
// last and replace whatever the request already carries, a name that would
// break authentication, the transport, or the provider's protocol is refused
// rather than sent: the provider's own credential headers, the headers the
// HTTP client and the gateway own, and the ones spec reserves for the type.
// Errors start with "headers" and name the header, never its value.
func validateHeaders(providerType string, spec DiscoveryConfig, headers map[string]string) error {
	if len(headers) == 0 {
		return nil
	}
	if spec.HeadersUnsupported {
		return fmt.Errorf("headers: provider type %q does not support headers", providerType)
	}
	if len(headers) > maxProviderHeaders {
		return fmt.Errorf("headers: %d headers set, at most %d are allowed", len(headers), maxProviderHeaders)
	}

	seen := make(map[string]string, len(headers))
	size := 0
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		value := headers[name]
		size += len(name) + len(value)
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("headers: %q is not a valid header name", name)
		}
		canonical := http.CanonicalHeaderKey(name)
		if first, dup := seen[canonical]; dup {
			return fmt.Errorf("headers.%s: header is set twice, also as %s", name, first)
		}
		seen[canonical] = name

		switch {
		case core.IsProviderCredentialHeader(name):
			return fmt.Errorf("headers.%s: header carries the provider credential; set api_key or api_keys instead", name)
		case core.IsProviderHeaderReserved(name):
			return fmt.Errorf("headers.%s: header is set by GoModel and cannot be configured", name)
		case headerReservedForType(spec, canonical):
			return fmt.Errorf("headers.%s: header is set by GoModel for %s providers and cannot be configured", name, providerType)
		case strings.TrimSpace(value) == "":
			return fmt.Errorf("headers.%s: value is empty", name)
		case !httpguts.ValidHeaderFieldValue(value):
			return fmt.Errorf("headers.%s: value holds a character HTTP does not allow in a header, such as a line break", name)
		}
	}
	if size > maxProviderHeaderBytes {
		return fmt.Errorf("headers: names and values take %d bytes, at most %d are allowed", size, maxProviderHeaderBytes)
	}
	return nil
}

// headerReservedForType reports whether spec reserves the canonical header
// name, reading "_" as "-" like the lists every type shares.
func headerReservedForType(spec DiscoveryConfig, canonical string) bool {
	canonical = strings.ReplaceAll(canonical, "_", "-")
	return slices.ContainsFunc(spec.ReservedHeaders, func(reserved string) bool {
		return strings.EqualFold(reserved, canonical)
	})
}

func providerHeaderPath(provider, header string) string {
	return "providers." + provider + ".headers." + header
}
