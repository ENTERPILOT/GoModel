package core

import (
	"net/http"
	"strings"
)

// BodyMode describes the transport shape expected for an endpoint.
type BodyMode string

const (
	BodyModeNone      BodyMode = "none"
	BodyModeJSON      BodyMode = "json"
	BodyModeMultipart BodyMode = "multipart"
	BodyModeOpaque    BodyMode = "opaque"
)

// Operation identifies the gateway operation represented by an endpoint.
type Operation string

const (
	OperationChatCompletions     Operation = "chat_completions"
	OperationResponses           Operation = "responses"
	OperationConversations       Operation = "conversations"
	OperationEmbeddings          Operation = "embeddings"
	OperationBatches             Operation = "batches"
	OperationFiles               Operation = "files"
	OperationAudioSpeech         Operation = "audio_speech"
	OperationAudioTranscriptions Operation = "audio_transcriptions"
	OperationRealtime            Operation = "realtime"
	OperationProviderPassthrough Operation = "provider_passthrough"
	OperationMCP                 Operation = "mcp"
)

// EndpointDescriptor centralizes the transport-facing classification of model and provider routes.
type EndpointDescriptor struct {
	ModelInteraction bool
	IngressManaged   bool
	Dialect          string
	Operation        Operation
	BodyMode         BodyMode
}

// DescribeEndpoint classifies a request path and method for ADR-0002 ingress handling.
func DescribeEndpoint(method, path string) EndpointDescriptor {
	desc := describeEndpointPath(path)
	desc.BodyMode = bodyModeForEndpoint(method, path, desc.Operation)
	return desc
}

// DescribeEndpointPath classifies a request path for ADR-0002 ingress handling.
func DescribeEndpointPath(path string) EndpointDescriptor {
	desc := describeEndpointPath(path)
	desc.BodyMode = bodyModeForEndpoint("", path, desc.Operation)
	return desc
}

func describeEndpointPath(path string) EndpointDescriptor {
	path = normalizeEndpointPath(path)

	switch {
	case path == "/v1/chat/completions":
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        OperationChatCompletions,
		}
	case matchesEndpointPath(path, "/v1/responses"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        OperationResponses,
		}
	case path == "/v1/conversations" || strings.HasPrefix(path, "/v1/conversations/"):
		// Conversations are a gateway-managed resource store (no provider
		// call), but they are an ingress-managed /v1 endpoint and are
		// classified as a model interaction path so they appear in request
		// and audit logs alongside the other /v1 endpoints.
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        OperationConversations,
		}
	case path == "/v1/embeddings":
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        OperationEmbeddings,
		}
	case path == "/v1/messages" || path == "/v1/messages/count_tokens":
		// Anthropic Messages dialect. It is translated to the canonical chat
		// type at ingress and runs through the chat-completions pipeline, so it
		// is classified as a chat-completions operation (see ADR-0007).
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "anthropic",
			Operation:        OperationChatCompletions,
		}
	case path == "/v1/batches" || strings.HasPrefix(path, "/v1/batches/"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        OperationBatches,
		}
	case path == "/v1/files" || strings.HasPrefix(path, "/v1/files/"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "openai_compat",
			Operation:        OperationFiles,
		}
	case path == "/v1/audio/speech":
		// Audio endpoints call a provider and incur usage, so they are model
		// interactions and must appear in audit/request logs. They are not
		// IngressManaged: the handlers parse the body themselves (JSON for
		// speech, multipart for transcriptions) rather than going through the
		// translated inference pipeline.
		return EndpointDescriptor{
			ModelInteraction: true,
			Dialect:          "openai_compat",
			Operation:        OperationAudioSpeech,
		}
	case path == "/v1/audio/transcriptions":
		return EndpointDescriptor{
			ModelInteraction: true,
			Dialect:          "openai_compat",
			Operation:        OperationAudioTranscriptions,
		}
	case path == "/v1/realtime" || path == "/v1/realtime/calls" || path == "/v1/realtime/client_secrets":
		// The realtime endpoints relay the provider's schema verbatim: /v1/realtime
		// upgrades to a websocket, /v1/realtime/calls exchanges WebRTC SDP, and
		// /v1/realtime/client_secrets mints ephemeral browser credentials. They are
		// model interactions (incur usage, must be observable) but not
		// IngressManaged: the handlers parse or hijack the transport themselves
		// rather than going through the JSON inference pipeline.
		return EndpointDescriptor{
			ModelInteraction: true,
			Dialect:          "openai_compat",
			Operation:        OperationRealtime,
		}
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"):
		// The MCP gateway relays JSON-RPC tool traffic to upstream MCP servers.
		// It is a model interaction (incurs usage, must be observable, may hold
		// a long-lived SSE stream that needs the write deadline cleared) but is
		// not IngressManaged: the MCP SDK handler owns the exchange rather than
		// the JSON inference pipeline.
		return EndpointDescriptor{
			ModelInteraction: true,
			Dialect:          "mcp",
			Operation:        OperationMCP,
		}
	case strings.HasPrefix(path, "/p/"):
		return EndpointDescriptor{
			ModelInteraction: true,
			IngressManaged:   true,
			Dialect:          "provider_passthrough",
			Operation:        OperationProviderPassthrough,
		}
	default:
		return EndpointDescriptor{}
	}
}

func bodyModeForEndpoint(method, path string, operation Operation) BodyMode {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = normalizeEndpointPath(path)

	switch operation {
	case OperationChatCompletions, OperationEmbeddings:
		return BodyModeJSON
	case OperationResponses:
		if method == http.MethodPost && (path == "/v1/responses" || path == "/v1/responses/input_tokens" || path == "/v1/responses/compact") {
			return BodyModeJSON
		}
		return BodyModeNone
	case OperationConversations:
		// POST creates (/v1/conversations) or updates (/v1/conversations/{id}).
		if method == http.MethodPost {
			return BodyModeJSON
		}
		return BodyModeNone
	case OperationBatches:
		switch method {
		case http.MethodPost:
			if strings.HasSuffix(path, "/cancel") {
				return BodyModeNone
			}
			return BodyModeJSON
		default:
			return BodyModeNone
		}
	case OperationFiles:
		if method == http.MethodPost && path == "/v1/files" {
			return BodyModeMultipart
		}
		return BodyModeNone
	case OperationAudioSpeech:
		return BodyModeJSON
	case OperationAudioTranscriptions:
		return BodyModeMultipart
	case OperationRealtime:
		if method == http.MethodPost && path == "/v1/realtime/client_secrets" {
			return BodyModeJSON
		}
		if method == http.MethodPost && path == "/v1/realtime/calls" {
			// SDP exchange bodies are application/sdp or multipart form data.
			return BodyModeOpaque
		}
		return BodyModeNone
	case OperationMCP:
		if method == http.MethodPost {
			return BodyModeJSON
		}
		return BodyModeNone
	case OperationProviderPassthrough:
		return BodyModeOpaque
	default:
		return BodyModeNone
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

func normalizeEndpointPath(path string) string {
	path, _, _ = strings.Cut(strings.TrimSpace(path), "?")
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return path
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
