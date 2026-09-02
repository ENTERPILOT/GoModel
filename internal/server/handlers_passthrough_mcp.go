package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// ProviderPassthrough handles opaque provider-native requests under /p/{provider}/{endpoint}.
//
// OpenAI and Anthropic are the first-class providers in this ADR-0002 slice. Other
// providers are intentionally deferred until they fit the same low-friction opaque path.
//
// @Summary      Provider passthrough
// @Description  Runtime-configurable passthrough endpoint under /p/{provider}/{endpoint}; enabled by default via server.enable_passthrough_routes. The endpoint path is opaque and may proxy JSON, binary, or SSE responses with upstream status codes preserved. For multi-segment provider endpoints, clients that rely on OpenAPI-generated path handling should URL-encode embedded slashes in the endpoint parameter. A leading v1/ segment is normalized away by default so /p/{provider}/v1/... and /p/{provider}/... map to the same upstream path relative to the provider base URL.
// @Tags         passthrough
// @Accept       json
// @Accept       mpfd
// @Produce      json
// @Produce      application/octet-stream
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        provider  path      string  true  "Provider type"
// @Param        endpoint  path      string  true  "Provider-native endpoint path relative to the provider base URL. URL-encode embedded / characters when using generated clients."
// @Success      200       {file}    file    "Opaque upstream response body"
// @Success      201       {file}    file    "Opaque upstream response body"
// @Success      202       {file}    file    "Opaque upstream response body"
// @Success      204       {string}  string  "No Content passthrough response"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /p/{provider}/{endpoint} [get]
// @Router       /p/{provider}/{endpoint} [post]
// @Router       /p/{provider}/{endpoint} [put]
// @Router       /p/{provider}/{endpoint} [patch]
// @Router       /p/{provider}/{endpoint} [delete]
// @Router       /p/{provider}/{endpoint} [head]
// @Router       /p/{provider}/{endpoint} [options]
func (h *Handler) ProviderPassthrough(c *echo.Context) error {
	// A websocket upgrade on a passthrough route is a realtime session, not an
	// HTTP proxy request; relay it through the realtime service instead.
	if isWebSocketUpgrade(c.Request()) {
		providerType, _, endpoint, _, err := passthroughExecutionTarget(c, h.provider, h.normalizePassthroughV1Prefix)
		if err != nil {
			return handleError(c, err)
		}
		// Realtime upgrades honor the same provider allowlist as the HTTP
		// passthrough path: a provider disabled for /p/{provider}/... must not be
		// reachable via a websocket upgrade.
		if !isEnabledPassthroughProvider(providerType, h.enabledPassthroughProviders) {
			return handleError(c, h.passthrough().unsupportedPassthroughProviderError(providerType))
		}
		// endpoint may carry the query string (e.g. "realtime?model=..."); compare
		// only the path segment. The translations path selects the translation
		// session surface, exactly as the typed route does.
		endpointPath := strings.Trim(strings.SplitN(endpoint, "?", 2)[0], "/")
		intent := ""
		switch endpointPath {
		case "realtime":
		case "realtime/translations":
			intent = core.RealtimeIntentTranslation
		default:
			return handleError(c, core.NewNotFoundError("unsupported realtime passthrough endpoint: "+endpointPath))
		}
		return h.realtime().PassthroughRealtime(c, providerType, intent)
	}
	return h.passthrough().ProviderPassthrough(c)
}

// MCP handles the aggregated MCP endpoint at /mcp.
//
// @Summary      MCP gateway (aggregated)
// @Description  Streamable-HTTP MCP endpoint aggregating every configured upstream MCP server visible to the caller. Tools and prompts are namespaced as {slug}_{name}. POST carries JSON-RPC messages, GET opens the server-notification SSE stream, DELETE ends the session. The X-MCP-Servers request header optionally narrows the visible servers to a comma-separated subset of server slugs.
// @Tags         mcp
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Success      200  {object}  map[string]interface{}  "JSON-RPC response or SSE stream"
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      429  {object}  core.OpenAIErrorEnvelope
// @Failure      501  {object}  core.OpenAIErrorEnvelope
// @Router       /mcp [post]
// @Router       /mcp [get]
// @Router       /mcp [delete]
func (h *Handler) MCP(c *echo.Context) error {
	return h.mcp().handle(c, "")
}

// MCPServer handles the per-server MCP endpoints at /mcp/{server}.
//
// @Summary      MCP gateway (single server)
// @Description  Streamable-HTTP MCP endpoint exposing one configured upstream MCP server with original (un-prefixed) tool names.
// @Tags         mcp
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        server  path      string  true  "Configured MCP server slug"
// @Success      200     {object}  map[string]interface{}  "JSON-RPC response or SSE stream"
// @Failure      401     {object}  core.OpenAIErrorEnvelope
// @Failure      404     {object}  core.OpenAIErrorEnvelope
// @Failure      429     {object}  core.OpenAIErrorEnvelope
// @Failure      501     {object}  core.OpenAIErrorEnvelope
// @Router       /mcp/{server} [post]
// @Router       /mcp/{server} [get]
// @Router       /mcp/{server} [delete]
func (h *Handler) MCPServer(c *echo.Context) error {
	return h.mcp().handle(c, c.Param("server"))
}
