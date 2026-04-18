package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/responsestore"
)

// nativeResponseService owns Responses lifecycle and utility endpoints. Create
// still uses translatedInferenceService because it participates in fallback,
// usage, streaming, and response-cache behavior.
type nativeResponseService struct {
	provider                 core.RoutableProvider
	modelResolver            RequestModelResolver
	modelAuthorizer          RequestModelAuthorizer
	workflowPolicyResolver   RequestWorkflowPolicyResolver
	translatedRequestPatcher TranslatedRequestPatcher
	responseStore            responsestore.Store
}

func (s *nativeResponseService) GetResponse(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := responseIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	params, err := responseRetrieveParamsFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	if params.Stream {
		return handleError(c, unsupportedResponseOperation("streaming response retrieval is not supported"))
	}

	stored, err := s.loadStoredResponse(ctx, id)
	if err == nil {
		auditResponseEntry(c, storedProvider(stored))
		return c.JSON(http.StatusOK, stored.Response)
	}
	if err != nil && !errors.Is(err, responsestore.ErrNotFound) {
		return handleError(c, err)
	}

	resp, providerType, err := s.getNativeResponse(ctx, responseProviderFromRequest(c), id, params)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
	}
	auditResponseEntry(c, providerType)
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeResponseService) ListResponseInputItems(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := responseIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	params, err := responseInputItemsParamsFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	stored, err := s.loadStoredResponse(ctx, id)
	if err == nil {
		auditResponseEntry(c, storedProvider(stored))
		return c.JSON(http.StatusOK, paginateStoredResponseInputItems(stored.InputItems, params))
	}
	if err != nil && !errors.Is(err, responsestore.ErrNotFound) {
		return handleError(c, err)
	}

	resp, providerType, err := s.listNativeResponseInputItems(ctx, responseProviderFromRequest(c), id, params)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
	}
	if resp.Object == "" {
		resp.Object = "list"
	}
	auditResponseEntry(c, providerType)
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeResponseService) CancelResponse(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := responseIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	stored, err := s.loadStoredResponse(ctx, id)
	if err == nil {
		providerType := storedProvider(stored)
		providerRoute := storedProviderRoute(stored)
		auditResponseEntry(c, providerType)
		resp, err := s.cancelNativeResponse(ctx, providerRoute, firstNonEmpty(stored.ProviderResponseID, id))
		if err != nil {
			if isUnsupportedNativeResponseError(err) {
				return handleError(c, unsupportedResponseOperation("native response cancellation is not available for this provider"))
			}
			return handleError(c, err)
		}
		if resp == nil {
			return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
		}
		normalizeCanceledResponse(resp, id, providerType)
		stored.Response = resp
		stored.Provider = providerType
		if updateErr := s.responseStore.Update(ctx, stored); updateErr != nil && !errors.Is(updateErr, responsestore.ErrNotFound) {
			return handleError(c, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to persist cancelled response", updateErr))
		}
		return c.JSON(http.StatusOK, resp)
	}
	if err != nil && !errors.Is(err, responsestore.ErrNotFound) {
		return handleError(c, err)
	}

	resp, providerType, err := s.cancelNativeResponseByRequest(ctx, responseProviderFromRequest(c), id)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
	}
	normalizeCanceledResponse(resp, id, providerType)
	auditResponseEntry(c, providerType)
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeResponseService) DeleteResponse(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := responseIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	stored, err := s.loadStoredResponse(ctx, id)
	if err == nil {
		providerType := storedProvider(stored)
		providerRoute := storedProviderRoute(stored)
		auditResponseEntry(c, providerType)
		deleteResp, deleteErr := s.deleteNativeResponse(ctx, providerRoute, firstNonEmpty(stored.ProviderResponseID, id))
		if deleteErr != nil && !isUnsupportedNativeResponseError(deleteErr) && !isNotFoundGatewayError(deleteErr) {
			return handleError(c, deleteErr)
		}
		if err := s.responseStore.Delete(ctx, id); err != nil && !errors.Is(err, responsestore.ErrNotFound) {
			return handleError(c, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to delete response", err))
		}
		if deleteResp == nil {
			deleteResp = &core.ResponseDeleteResponse{ID: id, Object: "response", Deleted: true}
		}
		deleteResp.ID = id
		if deleteResp.Object == "" {
			deleteResp.Object = "response"
		}
		deleteResp.Deleted = true
		return c.JSON(http.StatusOK, deleteResp)
	}
	if err != nil && !errors.Is(err, responsestore.ErrNotFound) {
		return handleError(c, err)
	}

	resp, providerType, err := s.deleteNativeResponseByRequest(ctx, responseProviderFromRequest(c), id)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
	}
	if resp.ID == "" {
		resp.ID = id
	}
	if resp.Object == "" {
		resp.Object = "response"
	}
	auditResponseEntry(c, providerType)
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeResponseService) CountResponseInputTokens(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))

	req, providerType, err := s.utilityRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	auditResponseEntry(c, providerType)

	router, ok := s.provider.(core.NativeResponseUtilityRoutableProvider)
	if !ok {
		return handleError(c, unsupportedResponseOperation("response input token counting is not supported by the current provider router"))
	}
	resp, err := router.CountResponseInputTokens(ctx, providerType, req)
	if err != nil {
		if isUnsupportedNativeResponseError(err) {
			return handleError(c, unsupportedResponseOperation("response input token counting is not supported by this provider"))
		}
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
	}
	if resp.Object == "" {
		resp.Object = "response.input_tokens"
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeResponseService) CompactResponse(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))

	req, providerType, err := s.utilityRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	auditResponseEntry(c, providerType)

	router, ok := s.provider.(core.NativeResponseUtilityRoutableProvider)
	if !ok {
		return handleError(c, unsupportedResponseOperation("response compaction is not supported by the current provider router"))
	}
	resp, err := router.CompactResponse(ctx, providerType, req)
	if err != nil {
		if isUnsupportedNativeResponseError(err) {
			return handleError(c, unsupportedResponseOperation("response compaction is not supported by this provider"))
		}
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty response", nil))
	}
	if resp.Object == "" {
		resp.Object = "response.compaction"
	}
	if resp.Provider == "" {
		resp.Provider = providerType
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *nativeResponseService) utilityRequest(c *echo.Context) (*core.ResponsesRequest, string, error) {
	req, err := canonicalJSONRequestFromSemantics[*core.ResponsesRequest](c, core.DecodeResponsesRequest)
	if err != nil {
		return nil, "", core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	if req == nil {
		return nil, "", core.NewInvalidRequestError("responses request body is required", errors.New("responses request body is null"))
	}

	workflow, err := ensureTranslatedRequestWorkflowWithAuthorizer(c, s.provider, s.modelResolver, s.modelAuthorizer, s.workflowPolicyResolver, &req.Model, &req.Provider)
	if err != nil {
		return nil, "", err
	}

	if s.translatedRequestPatcher != nil {
		req, err = s.translatedRequestPatcher.PatchResponsesRequest(c.Request().Context(), req)
		if err != nil {
			return nil, "", err
		}
	}

	providerType := ""
	if workflow != nil {
		providerType = strings.TrimSpace(workflow.ProviderType)
	}
	if providerType == "" {
		return nil, "", unsupportedResponseOperation("unable to resolve provider for response utility operation")
	}
	return req, providerType, nil
}

func (s *nativeResponseService) loadStoredResponse(ctx context.Context, id string) (*responsestore.StoredResponse, error) {
	if s.responseStore == nil {
		return nil, responsestore.ErrNotFound
	}
	stored, err := s.responseStore.Get(ctx, id)
	if err != nil {
		if errors.Is(err, responsestore.ErrNotFound) {
			return nil, err
		}
		return nil, core.NewProviderError("response_store", http.StatusInternalServerError, "failed to load response", err)
	}
	if stored == nil || stored.Response == nil {
		return nil, core.NewProviderError("response_store", http.StatusInternalServerError, "stored response payload missing", nil)
	}
	return stored, nil
}

func (s *nativeResponseService) getNativeResponse(ctx context.Context, providerType, id string, params core.ResponseRetrieveParams) (*core.ResponsesResponse, string, error) {
	return nativeResponseByProvider(ctx, s.provider, providerType, func(router core.NativeResponseLifecycleRoutableProvider, candidate string) (*core.ResponsesResponse, error) {
		return router.GetResponse(ctx, candidate, id, params)
	})
}

func (s *nativeResponseService) listNativeResponseInputItems(ctx context.Context, providerType, id string, params core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, string, error) {
	return nativeResponseByProvider(ctx, s.provider, providerType, func(router core.NativeResponseLifecycleRoutableProvider, candidate string) (*core.ResponseInputItemListResponse, error) {
		return router.ListResponseInputItems(ctx, candidate, id, params)
	})
}

func (s *nativeResponseService) cancelNativeResponse(ctx context.Context, providerType, id string) (*core.ResponsesResponse, error) {
	router, ok := s.provider.(core.NativeResponseLifecycleRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("response lifecycle routing is not supported by the current provider router", nil)
	}
	return router.CancelResponse(ctx, providerType, id)
}

func (s *nativeResponseService) cancelNativeResponseByRequest(ctx context.Context, providerType, id string) (*core.ResponsesResponse, string, error) {
	return nativeResponseByProvider(ctx, s.provider, providerType, func(router core.NativeResponseLifecycleRoutableProvider, candidate string) (*core.ResponsesResponse, error) {
		return router.CancelResponse(ctx, candidate, id)
	})
}

func normalizeCanceledResponse(resp *core.ResponsesResponse, id, providerType string) {
	if resp == nil {
		return
	}
	resp.ID = id
	resp.Object = "response"
	resp.Provider = providerType
}

func (s *nativeResponseService) deleteNativeResponse(ctx context.Context, providerType, id string) (*core.ResponseDeleteResponse, error) {
	router, ok := s.provider.(core.NativeResponseLifecycleRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("response lifecycle routing is not supported by the current provider router", nil)
	}
	return router.DeleteResponse(ctx, providerType, id)
}

func (s *nativeResponseService) deleteNativeResponseByRequest(ctx context.Context, providerType, id string) (*core.ResponseDeleteResponse, string, error) {
	return nativeResponseByProvider(ctx, s.provider, providerType, func(router core.NativeResponseLifecycleRoutableProvider, candidate string) (*core.ResponseDeleteResponse, error) {
		return router.DeleteResponse(ctx, candidate, id)
	})
}

func nativeResponseByProvider[T any](
	ctx context.Context,
	provider core.RoutableProvider,
	providerType string,
	call func(core.NativeResponseLifecycleRoutableProvider, string) (T, error),
) (T, string, error) {
	var zero T
	router, ok := provider.(core.NativeResponseLifecycleRoutableProvider)
	if !ok {
		return zero, "", unsupportedResponseOperation("response lifecycle routing is not supported by the current provider router")
	}

	providerType = strings.TrimSpace(providerType)
	if providerType != "" {
		result, err := call(router, providerType)
		return result, providerType, err
	}

	providers, err := nativeResponseProviderTypes(provider)
	if err != nil {
		return zero, "", err
	}
	if len(providers) == 0 {
		return zero, "", core.NewNotFoundError("response not found")
	}

	var firstErr error
	for _, candidate := range providers {
		if err := ctx.Err(); err != nil {
			return zero, "", responseLifecycleContextError(err)
		}
		result, err := call(router, candidate)
		if err == nil {
			return result, candidate, nil
		}
		if isNotFoundGatewayError(err) || isUnsupportedNativeResponseError(err) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return zero, "", firstErr
	}
	return zero, "", core.NewNotFoundError("response not found")
}

func responseLifecycleContextError(err error) *core.GatewayError {
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewProviderError("response_lifecycle", http.StatusGatewayTimeout, "response lifecycle request timed out", err)
	}
	return core.NewInvalidRequestErrorWithStatus(http.StatusRequestTimeout, "response lifecycle request canceled", err)
}

func nativeResponseProviderTypes(provider core.RoutableProvider) ([]string, error) {
	typed, ok := provider.(core.NativeResponseProviderTypeLister)
	if !ok {
		return nil, unsupportedResponseOperation("response lifecycle provider inventory is unavailable")
	}
	return typed.NativeResponseProviderTypes(), nil
}

func responseIDFromRequest(c *echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return "", core.NewInvalidRequestError("response id is required", nil)
	}
	return id, nil
}

func responseProviderFromRequest(c *echo.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.QueryParam("provider"))
}

func responseRetrieveParamsFromRequest(c *echo.Context) (core.ResponseRetrieveParams, error) {
	query := c.Request().URL.Query()
	params := core.ResponseRetrieveParams{
		Include: appendQueryArray(query["include"], query["include[]"]),
	}
	if raw := strings.TrimSpace(query.Get("include_obfuscation")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return params, core.NewInvalidRequestError("include_obfuscation must be a boolean", err).WithParam("include_obfuscation")
		}
		params.IncludeObfuscation = &parsed
	}
	if raw := strings.TrimSpace(query.Get("starting_after")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return params, core.NewInvalidRequestError("starting_after must be an integer", err).WithParam("starting_after")
		}
		params.StartingAfter = &parsed
	}
	if raw := strings.TrimSpace(query.Get("stream")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return params, core.NewInvalidRequestError("stream must be a boolean", err).WithParam("stream")
		}
		params.Stream = parsed
	}
	return params, nil
}

func responseInputItemsParamsFromRequest(c *echo.Context) (core.ResponseInputItemsParams, error) {
	query := c.Request().URL.Query()
	params := core.ResponseInputItemsParams{
		After:   strings.TrimSpace(query.Get("after")),
		Include: appendQueryArray(query["include"], query["include[]"]),
		Limit:   20,
		Order:   "desc",
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return params, core.NewInvalidRequestError("limit must be an integer", err).WithParam("limit")
		}
		switch {
		case limit <= 0:
			params.Limit = 20
		case limit > 100:
			params.Limit = 100
		default:
			params.Limit = limit
		}
	}
	if raw := strings.TrimSpace(query.Get("order")); raw != "" {
		if raw != "asc" && raw != "desc" {
			return params, core.NewInvalidRequestError("order must be asc or desc", nil).WithParam("order")
		}
		params.Order = raw
	}
	return params, nil
}

func appendQueryArray(values ...[]string) []string {
	var result []string
	for _, group := range values {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			result = append(result, value)
		}
	}
	return result
}

func paginateStoredResponseInputItems(items []json.RawMessage, params core.ResponseInputItemsParams) core.ResponseInputItemListResponse {
	ordered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, core.CloneRawJSON(item))
	}
	if params.Order != "asc" {
		slices.Reverse(ordered)
	}

	start := 0
	if params.After != "" {
		idx := -1
		for i, item := range ordered {
			if responseInputItemID(item) == params.After {
				idx = i
				break
			}
		}
		if idx >= 0 {
			start = idx + 1
		} else {
			ordered = nil
		}
	}
	if start > len(ordered) {
		start = len(ordered)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	remaining := ordered[start:]
	hasMore := len(remaining) > limit
	if hasMore {
		remaining = remaining[:limit]
	}

	resp := core.ResponseInputItemListResponse{
		Object:  "list",
		Data:    remaining,
		HasMore: hasMore,
	}
	if len(remaining) > 0 {
		resp.FirstID = responseInputItemID(remaining[0])
		resp.LastID = responseInputItemID(remaining[len(remaining)-1])
	}
	return resp
}

func responseInputItemID(raw json.RawMessage) string {
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return ""
	}
	return strings.TrimSpace(item.ID)
}

func storedProvider(stored *responsestore.StoredResponse) string {
	if stored == nil {
		return ""
	}
	if stored.Provider != "" {
		return stored.Provider
	}
	if stored.Response != nil {
		return stored.Response.Provider
	}
	return ""
}

func storedProviderRoute(stored *responsestore.StoredResponse) string {
	if stored == nil {
		return ""
	}
	if stored.ProviderName != "" {
		return stored.ProviderName
	}
	return storedProvider(stored)
}

func unsupportedResponseOperation(message string) *core.GatewayError {
	return core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, message, nil).WithCode("unsupported_response_operation")
}

func isUnsupportedNativeResponseError(err error) bool {
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	if !ok {
		return false
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest && gatewayErr.HTTPStatusCode() != http.StatusNotImplemented {
		return false
	}
	message := strings.ToLower(gatewayErr.Message)
	return strings.Contains(message, "response lifecycle") ||
		strings.Contains(message, "response utility") ||
		strings.Contains(message, "native response") ||
		strings.Contains(message, "responses lifecycle") ||
		strings.Contains(message, "responses utility")
}

func auditResponseEntry(c *echo.Context, providerType string) {
	if c == nil {
		return
	}
	auditlog.EnrichEntry(c, "response", providerType)
}
