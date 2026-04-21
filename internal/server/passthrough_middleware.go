package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// PassthroughProviderResolutionMiddleware resolves the :provider URL parameter
// (the YAML instance name) to a concrete PassthroughProvider and stores the
// result in context. It rejects requests for providers that have passthrough
// disabled or that cannot be found.
//
// disabledInstances is a set of instance names for which passthrough is
// disabled via the passthrough_disabled: true YAML flag. An empty map means
// all configured providers may receive passthrough traffic.
func PassthroughProviderResolutionMiddleware(provider core.RoutableProvider, disabledInstances map[string]struct{}) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			instanceName, _, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path)
			if !ok {
				return handleError(c, core.NewInvalidRequestError("invalid provider passthrough path", nil))
			}
			instanceName = strings.TrimSpace(instanceName)
			if instanceName == "" {
				return handleError(c, core.NewInvalidRequestError("passthrough provider instance name is required", nil))
			}

			// Best-effort model extraction for audit before any body consumption.
			bestModel := passthroughBestEffortModel(c)

			if _, disabled := disabledInstances[instanceName]; disabled {
				return handleError(c, core.NewInvalidRequestError(
					"passthrough is disabled for provider "+instanceName, nil,
				))
			}

			resolver, ok := provider.(core.PassthroughProviderResolver)
			if !ok {
				return handleError(c, core.NewInvalidRequestError("provider does not support named passthrough resolution", nil))
			}

			pp, providerType, err := resolver.ResolvePassthroughByName(instanceName)
			if err != nil {
				return handleError(c, err)
			}

			auditlog.EnrichEntry(c, bestModel, providerType)
			auditlog.EnrichEntryWithResolvedRoute(c, bestModel, providerType, instanceName)

			setPassthroughResolution(c, instanceName, providerType, pp)
			return next(c)
		}
	}
}

// passthroughBestEffortModel does a non-destructive body peek to extract the
// "model" field for audit enrichment before the body has been formally read.
func passthroughBestEffortModel(c *echo.Context) string {
	body, _ := readAndRestoreBody(c.Request())
	return bestEffortModel(body)
}

// PassthroughGuardrailsMiddleware optionally runs request-side guardrails
// against the passthrough request body before it is forwarded upstream.
//
// The body is read from the (already-buffered by BodyLimit) request stream,
// decoded as a best-effort core.ChatRequest, and fed through patcher. If
// the guardrail rejects the request an error is returned. The original body
// is always restored so the handler can forward it unchanged.
//
// patcher may be nil, in which case the middleware is a no-op. Guardrail
// modifications to the decoded request are intentionally discarded — the
// passthrough body is forwarded as-is to preserve opaque semantics.
func PassthroughGuardrailsMiddleware(patcher TranslatedRequestPatcher) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if patcher == nil {
				return next(c)
			}

			body, err := readAndRestoreBody(c.Request())
			if err != nil || len(body) == 0 {
				return next(c)
			}

			var chatReq core.ChatRequest
			if jsonErr := json.Unmarshal(body, &chatReq); jsonErr != nil {
				return next(c)
			}

			if _, err := patcher.PatchChatRequest(c.Request().Context(), &chatReq); err != nil {
				return handleError(c, err)
			}

			return next(c)
		}
	}
}

// readAndRestoreBody reads the full request body and replaces req.Body with a
// fresh reader over the same bytes so downstream handlers can read it again.
func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
