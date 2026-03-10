package core

import "strings"

// BodyMode describes the transport shape expected for an endpoint.
type BodyMode string

const (
	BodyModeNone      BodyMode = "none"
	BodyModeJSON      BodyMode = "json"
	BodyModeMultipart BodyMode = "multipart"
	BodyModeOpaque    BodyMode = "opaque"
)

// EndpointDescriptor centralizes the transport-facing classification of model and provider routes.
type EndpointDescriptor struct {
	ModelInteraction bool
	IngressManaged   bool
	Dialect          string
	Operation        string
	BodyMode         BodyMode
}

// DescribeEndpointPath classifies a request path for ADR-0002 ingress handling.
func DescribeEndpointPath(path string) EndpointDescriptor {
	path, _, _ = strings.Cut(strings.TrimSpace(path), "?")

	switch {
	case path == "/v1/chat/completions":
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        "chat_completions",
			BodyMode:         BodyModeJSON,
		}
	case matchesEndpointPath(path, "/v1/responses"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        "responses",
			BodyMode:         BodyModeJSON,
		}
	case path == "/v1/embeddings":
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        "embeddings",
			BodyMode:         BodyModeJSON,
		}
	case path == "/v1/batches" || strings.HasPrefix(path, "/v1/batches/"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        "batches",
			BodyMode:         BodyModeJSON,
		}
	case path == "/v1/files" || strings.HasPrefix(path, "/v1/files/"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        "files",
			BodyMode:         BodyModeMultipart,
		}
	case strings.HasPrefix(path, "/p/"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "provider_passthrough",
			Operation:        "provider_passthrough",
			BodyMode:         BodyModeOpaque,
		}
	default:
		return EndpointDescriptor{}
	}
}

func matchesEndpointPath(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	next := path[len(prefix):]
	return strings.HasPrefix(next, "/")
}

// IsModelInteractionPath reports whether a path is a model/provider interaction route.
func IsModelInteractionPath(path string) bool {
	return DescribeEndpointPath(path).ModelInteraction
}

// ParseProviderPassthroughPath extracts provider and endpoint from /p/{provider}/{endpoint...}.
func ParseProviderPassthroughPath(path string) (provider string, endpoint string, ok bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(path), "/")
	if !strings.HasPrefix(trimmed, "p/") {
		return "", "", false
	}

	parts := strings.SplitN(strings.TrimPrefix(trimmed, "p/"), "/", 2)
	if len(parts) == 0 {
		return "", "", false
	}

	provider = strings.TrimSpace(parts[0])
	if provider == "" {
		return "", "", false
	}

	if len(parts) == 2 {
		endpoint = strings.TrimSpace(parts[1])
	}
	return provider, endpoint, true
}
