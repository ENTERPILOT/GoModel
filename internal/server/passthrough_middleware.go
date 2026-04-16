package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

const goModelRequestIDHeader = "X-GoModel-Request-ID"

// GoModelRequestIDMiddleware assigns a unique X-GoModel-Request-ID to every
// passthrough request. It reads a client-supplied value first; if absent it
// generates a UUID. The value is stamped on both the inbound context (via
// setPassthroughRequestID) and the outbound response header.
func GoModelRequestIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			id := strings.TrimSpace(c.Request().Header.Get(goModelRequestIDHeader))
			if id == "" {
				id = uuid.NewString()
			}
			setPassthroughRequestID(c, id)
			c.Response().Header().Set(goModelRequestIDHeader, id)
			return next(c)
		}
	}
}

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
			instanceName, _, _ := core.ParseProviderPassthroughPath(c.Request().URL.Path)
			instanceName = strings.TrimSpace(instanceName)
			if instanceName == "" {
				return handleError(c, core.NewInvalidRequestError("passthrough provider instance name is required", nil))
			}

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

			setPassthroughResolution(c, instanceName, providerType, pp)
			return next(c)
		}
	}
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
