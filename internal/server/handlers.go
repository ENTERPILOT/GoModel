// Package server provides HTTP handlers and server setup for the LLM gateway.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

const (
	batchMetadataRequestIDKey   = "request_id"
	batchMetadataUsageLoggedKey = "usage_logged_at"
)

var batchResultsPending404Providers = map[string]struct{}{
	"anthropic": {},
}

// Handler holds the HTTP handlers
type Handler struct {
	provider                     core.RoutableProvider
	logger                       auditlog.LoggerInterface
	usageLogger                  usage.LoggerInterface
	pricingResolver              usage.PricingResolver
	batchStore                   batchstore.Store
	normalizePassthroughV1Prefix bool
}

// NewHandler creates a new handler with the given routable provider (typically the Router)
func NewHandler(provider core.RoutableProvider, logger auditlog.LoggerInterface, usageLogger usage.LoggerInterface, pricingResolver usage.PricingResolver) *Handler {
	return &Handler{
		provider:                     provider,
		logger:                       logger,
		usageLogger:                  usageLogger,
		pricingResolver:              pricingResolver,
		batchStore:                   batchstore.NewMemoryStore(),
		normalizePassthroughV1Prefix: true,
	}
}

// SetBatchStore replaces the batch store used by lifecycle endpoints.
// nil is ignored to keep an always-available fallback memory store.
func (h *Handler) SetBatchStore(store batchstore.Store) {
	if store == nil {
		return
	}
	h.batchStore = store
}

// handleStreamingResponse handles SSE streaming responses for both ChatCompletion and Responses endpoints.
// It wraps the stream with audit logging and usage tracking, and sets appropriate SSE headers.
func (h *Handler) handleStreamingResponse(c *echo.Context, model, provider string, streamFn func() (io.ReadCloser, error)) error {
	// Call streamFn first - only mark as streaming after success
	// This ensures failed streams are logged normally by handleError/middleware
	stream, err := streamFn()
	if err != nil {
		return handleError(c, err)
	}

	// Mark as streaming so middleware doesn't log (StreamLogWrapper handles it)
	auditlog.MarkEntryAsStreaming(c, true)
	auditlog.EnrichEntryWithStream(c, true)

	// Get entry from context and wrap stream for logging
	entry := auditlog.GetStreamEntryFromContext(c)
	streamEntry := auditlog.CreateStreamEntry(entry)
	if streamEntry != nil {
		streamEntry.StatusCode = http.StatusOK // Streaming always starts with 200 OK
	}
	wrappedStream := auditlog.WrapStreamForLogging(stream, h.logger, streamEntry, c.Request().URL.Path)

	// Wrap with usage tracking if enabled
	requestID := c.Request().Header.Get("X-Request-ID")
	endpoint := c.Request().URL.Path
	wrappedStream = usage.WrapStreamForUsage(wrappedStream, h.usageLogger, model, provider, requestID, endpoint, h.pricingResolver)

	defer func() {
		_ = wrappedStream.Close() //nolint:errcheck
	}()

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")

	// Capture response headers on stream entry AFTER setting them
	if streamEntry != nil && streamEntry.Data != nil {
		streamEntry.Data.ResponseHeaders = map[string]string{
			"Content-Type":  "text/event-stream",
			"Cache-Control": "no-cache",
			"Connection":    "keep-alive",
		}
	}

	c.Response().WriteHeader(http.StatusOK)
	if err := flushStream(c.Response(), wrappedStream); err != nil {
		recordStreamingError(streamEntry, model, provider, c.Request().URL.Path, requestID, err)
	}
	return nil
}

func flushStream(w io.Writer, stream io.Reader) error {
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func recordStreamingError(streamEntry *auditlog.LogEntry, model, provider, path, requestID string, err error) {
	if streamEntry != nil {
		streamEntry.ErrorType = "stream_error"
		if streamEntry.Data == nil {
			streamEntry.Data = &auditlog.LogData{}
		}
		streamEntry.Data.ErrorMessage = err.Error()
	}

	slog.Warn("stream terminated abnormally",
		"error", err,
		"model", model,
		"provider", provider,
		"path", path,
		"request_id", requestID,
	)
}

func (h *Handler) logUsage(model, providerType string, extractFn func(*core.ModelPricing) *usage.UsageEntry) {
	if h.usageLogger == nil || !h.usageLogger.Config().Enabled {
		return
	}
	var pricing *core.ModelPricing
	if h.pricingResolver != nil {
		pricing = h.pricingResolver.ResolvePricing(model, providerType)
	}
	if entry := extractFn(pricing); entry != nil {
		h.usageLogger.Write(entry)
	}
}

func resolveModelSelector(ctx context.Context, model, provider *string) error {
	return core.NormalizeModelSelector(core.GetSemanticEnvelope(ctx), model, provider)
}

func isSupportedPassthroughProvider(providerType string) bool {
	switch strings.TrimSpace(providerType) {
	case "openai", "anthropic":
		return true
	default:
		return false
	}
}

func normalizePassthroughEndpoint(endpoint string, enabled bool) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case endpoint == "v1":
		if !enabled {
			return "", core.NewInvalidRequestError("provider passthrough v1 alias is disabled; use /p/{provider}/... without the v1 prefix", nil)
		}
		return "", nil
	case strings.HasPrefix(endpoint, "v1/"):
		if !enabled {
			return "", core.NewInvalidRequestError("provider passthrough v1 alias is disabled; use /p/{provider}/... without the v1 prefix", nil)
		}
		return strings.TrimPrefix(endpoint, "v1/"), nil
	default:
		return endpoint, nil
	}
}

func (h *Handler) passthroughEndpoint(c *echo.Context) (string, string, error) {
	providerType, endpoint, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path)
	if !ok {
		return "", "", core.NewInvalidRequestError("invalid provider passthrough path", nil)
	}
	endpoint, err := normalizePassthroughEndpoint(endpoint, h.normalizePassthroughV1Prefix)
	if err != nil {
		return "", "", err
	}
	if endpoint == "" {
		return "", "", core.NewInvalidRequestError("provider passthrough endpoint is required", nil)
	}
	if rawQuery := strings.TrimSpace(c.Request().URL.RawQuery); rawQuery != "" {
		endpoint += "?" + rawQuery
	}
	return providerType, endpoint, nil
}

func buildPassthroughHeaders(src http.Header) map[string]string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]string, len(src))
	for key, values := range src {
		if skipPassthroughHeader(key) || len(values) == 0 {
			continue
		}
		dst[key] = strings.Join(values, ", ")
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func skipPassthroughHeader(key string) bool {
	switch http.CanonicalHeaderKey(strings.TrimSpace(key)) {
	case "Authorization", "X-Api-Key", "Host", "Content-Length", "Connection", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func copyPassthroughResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if skipPassthroughHeader(key) || len(values) == 0 {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isSSEContentType(headers map[string][]string) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "Content-Type") {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

func passthroughStreamAuditPath(requestPath, providerType, endpoint string) string {
	normalized := "/" + strings.TrimLeft(strings.SplitN(endpoint, "?", 2)[0], "/")
	if providerType == "openai" {
		switch normalized {
		case "/chat/completions":
			return "/v1/chat/completions"
		case "/responses":
			return "/v1/responses"
		}
	}
	return requestPath
}

func (h *Handler) proxyPassthroughResponse(c *echo.Context, providerType, endpoint string, resp *core.PassthroughResponse) error {
	if resp == nil || resp.Body == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty passthrough response", nil))
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	copyPassthroughResponseHeaders(c.Response().Header(), http.Header(resp.Headers))

	if isSSEContentType(resp.Headers) {
		auditlog.MarkEntryAsStreaming(c, true)
		auditlog.EnrichEntryWithStream(c, true)

		entry := auditlog.GetStreamEntryFromContext(c)
		streamEntry := auditlog.CreateStreamEntry(entry)
		if streamEntry != nil {
			streamEntry.StatusCode = resp.StatusCode
		}

		wrappedStream := auditlog.WrapStreamForLogging(resp.Body, h.logger, streamEntry, passthroughStreamAuditPath(c.Request().URL.Path, providerType, endpoint))
		defer func() {
			_ = wrappedStream.Close()
		}()

		c.Response().WriteHeader(resp.StatusCode)
		if err := flushStream(c.Response(), wrappedStream); err != nil {
			recordStreamingError(streamEntry, "", providerType, c.Request().URL.Path, c.Request().Header.Get("X-Request-ID"), err)
			return err
		}
		return nil
	}

	c.Response().WriteHeader(resp.StatusCode)
	if _, err := io.Copy(c.Response(), resp.Body); err != nil {
		return err
	}
	return nil
}

// ProviderPassthrough handles opaque provider-native requests under /p/{provider}/{endpoint}.
//
// OpenAI and Anthropic are the first-class providers in this ADR-0002 slice. Other
// providers are intentionally deferred until they fit the same low-friction opaque path.
//
// @Summary      Provider passthrough
// @Description  Runtime-configurable passthrough endpoint under /p/{provider}/{endpoint}; enabled by default via server.enable_provider_passthrough. A leading v1/ segment is normalized away by default so /p/{provider}/v1/... and /p/{provider}/... map to the same upstream path relative to the provider base URL.
// @Tags         passthrough
// @Accept       json
// @Accept       mpfd
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        provider  path      string  true  "Provider type"
// @Param        endpoint  path      string  true  "Provider-native endpoint path"
// @Success      200       {string}  string  "Raw upstream response"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Router       /p/{provider}/{endpoint} [get]
// @Router       /p/{provider}/{endpoint} [post]
// @Router       /p/{provider}/{endpoint} [put]
// @Router       /p/{provider}/{endpoint} [patch]
// @Router       /p/{provider}/{endpoint} [delete]
// @Router       /p/{provider}/{endpoint} [head]
// @Router       /p/{provider}/{endpoint} [options]
func (h *Handler) ProviderPassthrough(c *echo.Context) error {
	passthroughProvider, ok := h.provider.(core.PassthroughRoutableProvider)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}

	providerType, endpoint, err := h.passthroughEndpoint(c)
	if err != nil {
		return handleError(c, err)
	}
	if !isSupportedPassthroughProvider(providerType) {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is currently supported only for openai and anthropic", nil))
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("failed to read passthrough request body", err))
	}

	ctx := core.WithRequestID(c.Request().Context(), c.Request().Header.Get("X-Request-ID"))
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:   c.Request().Method,
		Endpoint: endpoint,
		Body:     bodyBytes,
		Headers:  buildPassthroughHeaders(c.Request().Header),
	})
	if err != nil {
		return handleError(c, err)
	}

	auditlog.EnrichEntry(c, "passthrough", providerType)
	return h.proxyPassthroughResponse(c, providerType, endpoint, resp)
}

// ChatCompletion handles POST /v1/chat/completions
//
// @Summary      Create a chat completion
// @Tags         chat
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        request  body      core.ChatRequest  true  "Chat completion request"
// @Success      200      {object}  core.ChatResponse  "JSON response or SSE stream when stream=true"
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      429      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Router       /v1/chat/completions [post]
func (h *Handler) ChatCompletion(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemanticEnvelope[*core.ChatRequest](c, core.DecodeChatRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if err := resolveModelSelector(c.Request().Context(), &req.Model, &req.Provider); err != nil {
		return handleError(c, err)
	}

	ctx, providerType := ModelCtx(c)
	requestID := c.Request().Header.Get("X-Request-ID")
	ctx = core.WithRequestID(ctx, requestID)

	if req.Stream {
		if h.usageLogger != nil && h.usageLogger.Config().EnforceReturningUsageData {
			if req.StreamOptions == nil {
				req.StreamOptions = &core.StreamOptions{}
			}
			req.StreamOptions.IncludeUsage = true
		}
		return h.handleStreamingResponse(c, req.Model, providerType, func() (io.ReadCloser, error) {
			return h.provider.StreamChatCompletion(ctx, req)
		})
	}

	resp, err := h.provider.ChatCompletion(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	h.logUsage(resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromChatResponse(resp, requestID, providerType, "/v1/chat/completions", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

// Health handles GET /health
//
// @Summary      Health check
// @Tags         system
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /health [get]
func (h *Handler) Health(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ListModels handles GET /v1/models
//
// @Summary      List available models
// @Tags         models
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  core.ModelsResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      502  {object}  core.GatewayError
// @Router       /v1/models [get]
func (h *Handler) ListModels(c *echo.Context) error {
	// Create context with request ID for provider
	requestID := c.Request().Header.Get("X-Request-ID")
	ctx := core.WithRequestID(c.Request().Context(), requestID)

	resp, err := h.provider.ListModels(ctx)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, resp)
}

func (h *Handler) nativeFileRouter() (core.NativeFileRoutableProvider, error) {
	nativeRouter, ok := h.provider.(core.NativeFileRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("file routing is not supported by the current provider router", nil)
	}
	return nativeRouter, nil
}

func (h *Handler) fileProviderTypes(ctx *echo.Context) ([]string, error) {
	resp, err := h.provider.ListModels(ctx.Request().Context())
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []string{}, nil
	}
	seen := make(map[string]struct{})
	providers := make([]string, 0)
	for _, model := range resp.Data {
		providerType := strings.TrimSpace(h.provider.GetProviderType(model.ID))
		if providerType == "" {
			continue
		}
		if _, exists := seen[providerType]; exists {
			continue
		}
		seen[providerType] = struct{}{}
		providers = append(providers, providerType)
	}
	sort.Strings(providers)
	return providers, nil
}

func (h *Handler) fileByID(
	c *echo.Context,
	callFn func(core.NativeFileRoutableProvider, string, string) (any, error),
	respondFn func(*echo.Context, any) error,
) error {
	nativeRouter, err := h.nativeFileRouter()
	if err != nil {
		return handleError(c, err)
	}

	fileReq, err := fileRequestFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}

	id := strings.TrimSpace(fileReq.FileID)
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("file id is required", nil))
	}

	if providerType := fileReq.Provider; providerType != "" {
		auditlog.EnrichEntry(c, "file", providerType)
		result, err := callFn(nativeRouter, providerType, id)
		if err != nil {
			return handleError(c, err)
		}
		return respondFn(c, result)
	}

	providers, err := h.fileProviderTypes(c)
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntry(c, "file", "")

	var firstErr error
	for _, candidate := range providers {
		result, err := callFn(nativeRouter, candidate, id)
		if err == nil {
			return respondFn(c, result)
		}
		if isNotFoundGatewayError(err) || isUnsupportedNativeFilesError(err) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return handleError(c, firstErr)
	}
	return handleError(c, core.NewNotFoundError("file not found: "+id))
}

func isNotFoundGatewayError(err error) bool {
	var gatewayErr *core.GatewayError
	return errors.As(err, &gatewayErr) && gatewayErr.HTTPStatusCode() == http.StatusNotFound
}

func isUnsupportedNativeFilesError(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(gatewayErr.Message), "does not support native file operations")
}

func sortFilesDesc(items []core.FileObject) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt > items[j].CreatedAt
	})
}

func applyAfterCursor(items []core.FileObject, after string) ([]core.FileObject, error) {
	after = strings.TrimSpace(after)
	if after == "" {
		return items, nil
	}
	for i := range items {
		if items[i].ID == after {
			if i+1 >= len(items) {
				return []core.FileObject{}, nil
			}
			return items[i+1:], nil
		}
	}
	return nil, core.NewNotFoundError("after cursor file not found: " + after)
}

// CreateFile handles POST /v1/files.
//
// @Summary      Upload a file
// @Tags         files
// @Accept       mpfd
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Provider override when multiple providers are configured"
// @Param        purpose   formData  string  true   "File purpose"
// @Param        file      formData  file    true   "File to upload"
// @Success      200       {object}  core.FileObject
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Router       /v1/files [post]
func (h *Handler) CreateFile(c *echo.Context) error {
	nativeRouter, err := h.nativeFileRouter()
	if err != nil {
		return handleError(c, err)
	}

	fileReq, err := fileRequestFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}

	providers, err := h.fileProviderTypes(c)
	if err != nil {
		return handleError(c, err)
	}

	providerType := fileReq.Provider
	if providerType == "" {
		if len(providers) == 1 {
			providerType = providers[0]
		} else if len(providers) == 0 {
			return handleError(c, core.NewInvalidRequestError("no providers are available for file uploads", nil))
		} else {
			return handleError(c, core.NewInvalidRequestError("provider is required when multiple providers are configured; pass ?provider=<type>", nil))
		}
	}
	auditlog.EnrichEntry(c, "file", providerType)

	purpose := strings.TrimSpace(fileReq.Purpose)
	if purpose == "" {
		return handleError(c, core.NewInvalidRequestError("purpose is required", nil))
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("file is required", err))
	}
	file, err := fileHeader.Open()
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("failed to open uploaded file", err))
	}
	defer func() {
		_ = file.Close()
	}()

	content, err := io.ReadAll(file)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("failed to read uploaded file", err))
	}

	requestID := strings.TrimSpace(c.Request().Header.Get("X-Request-ID"))
	ctx := core.WithRequestID(c.Request().Context(), requestID)
	filename := strings.TrimSpace(fileReq.Filename)
	if filename == "" {
		filename = fileHeader.Filename
	}
	resp, err := nativeRouter.CreateFile(ctx, providerType, &core.FileCreateRequest{
		Purpose:  purpose,
		Filename: filename,
		Content:  content,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

// ListFiles handles GET /v1/files.
//
// @Summary      List files
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Provider filter"
// @Param        purpose   query     string  false  "File purpose filter"
// @Param        after     query     string  false  "Pagination cursor"
// @Param        limit     query     int     false  "Maximum items to return (1-100, default 20)"
// @Success      200       {object}  core.FileListResponse
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Router       /v1/files [get]
func (h *Handler) ListFiles(c *echo.Context) error {
	nativeRouter, err := h.nativeFileRouter()
	if err != nil {
		return handleError(c, err)
	}

	fileReq, err := fileRequestFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}
	limit := 20
	if fileReq.HasLimit {
		limit = fileReq.Limit
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	purpose := fileReq.Purpose
	after := fileReq.After
	providerType := fileReq.Provider

	if providerType != "" {
		auditlog.EnrichEntry(c, "file", providerType)
		resp, err := nativeRouter.ListFiles(c.Request().Context(), providerType, purpose, limit, after)
		if err != nil {
			return handleError(c, err)
		}
		if resp == nil {
			resp = &core.FileListResponse{Object: "list"}
		}
		if resp.Object == "" {
			resp.Object = "list"
		}
		return c.JSON(http.StatusOK, resp)
	}

	providers, err := h.fileProviderTypes(c)
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntry(c, "file", "")

	aggregated := make([]core.FileObject, 0)
	anySuccess := false
	var firstErr error
	for _, candidate := range providers {
		resp, err := nativeRouter.ListFiles(c.Request().Context(), candidate, purpose, limit+1, "")
		if err != nil {
			if isUnsupportedNativeFilesError(err) || isNotFoundGatewayError(err) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		anySuccess = true
		if resp == nil {
			continue
		}
		aggregated = append(aggregated, resp.Data...)
	}
	if !anySuccess && firstErr != nil {
		return handleError(c, firstErr)
	}

	sortFilesDesc(aggregated)
	aggregated, err = applyAfterCursor(aggregated, after)
	if err != nil {
		return handleError(c, err)
	}
	hasMore := len(aggregated) > limit
	if hasMore {
		aggregated = aggregated[:limit]
	}

	return c.JSON(http.StatusOK, core.FileListResponse{
		Object:  "list",
		Data:    aggregated,
		HasMore: hasMore,
	})
}

// GetFile handles GET /v1/files/{id}.
//
// @Summary      Get file metadata
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "File ID"
// @Param        provider  query     string  false  "Provider override"
// @Success      200       {object}  core.FileObject
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Router       /v1/files/{id} [get]
func (h *Handler) GetFile(c *echo.Context) error {
	return h.fileByID(c,
		func(r core.NativeFileRoutableProvider, provider, id string) (any, error) {
			return r.GetFile(c.Request().Context(), provider, id)
		},
		func(c *echo.Context, result any) error {
			return c.JSON(http.StatusOK, result)
		},
	)
}

// DeleteFile handles DELETE /v1/files/{id}.
//
// @Summary      Delete a file
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "File ID"
// @Param        provider  query     string  false  "Provider override"
// @Success      200       {object}  core.FileDeleteResponse
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Router       /v1/files/{id} [delete]
func (h *Handler) DeleteFile(c *echo.Context) error {
	return h.fileByID(c,
		func(r core.NativeFileRoutableProvider, provider, id string) (any, error) {
			return r.DeleteFile(c.Request().Context(), provider, id)
		},
		func(c *echo.Context, result any) error {
			return c.JSON(http.StatusOK, result)
		},
	)
}

// GetFileContent handles GET /v1/files/{id}/content.
//
// @Summary      Download file content
// @Tags         files
// @Produce      application/octet-stream
// @Security     BearerAuth
// @Param        id        path   string  true   "File ID"
// @Param        provider  query  string  false  "Provider override"
// @Success      200       {file}  file  "Raw file content"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Router       /v1/files/{id}/content [get]
func (h *Handler) GetFileContent(c *echo.Context) error {
	return h.fileByID(c,
		func(r core.NativeFileRoutableProvider, provider, id string) (any, error) {
			return r.GetFileContent(c.Request().Context(), provider, id)
		},
		func(c *echo.Context, result any) error {
			resp := result.(*core.FileContentResponse)
			contentType := strings.TrimSpace(resp.ContentType)
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			return c.Blob(http.StatusOK, contentType, resp.Data)
		},
	)
}

// Responses handles POST /v1/responses
//
// @Summary      Create a model response (Responses API)
// @Tags         responses
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ResponsesRequest  true  "Responses API request"
// @Success      200      {object}  core.ResponsesResponse
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      429      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Router       /v1/responses [post]
func (h *Handler) Responses(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemanticEnvelope[*core.ResponsesRequest](c, core.DecodeResponsesRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if err := resolveModelSelector(c.Request().Context(), &req.Model, &req.Provider); err != nil {
		return handleError(c, err)
	}

	ctx, providerType := ModelCtx(c)
	requestID := c.Request().Header.Get("X-Request-ID")

	if req.Stream {
		return h.handleStreamingResponse(c, req.Model, providerType, func() (io.ReadCloser, error) {
			return h.provider.StreamResponses(ctx, req)
		})
	}

	resp, err := h.provider.Responses(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	h.logUsage(resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromResponsesResponse(resp, requestID, providerType, "/v1/responses", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

// Embeddings handles POST /v1/embeddings
//
// @Summary      Create embeddings
// @Tags         embeddings
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.EmbeddingRequest  true  "Embeddings request"
// @Success      200      {object}  core.EmbeddingResponse
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      429      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Router       /v1/embeddings [post]
func (h *Handler) Embeddings(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemanticEnvelope[*core.EmbeddingRequest](c, core.DecodeEmbeddingRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if err := resolveModelSelector(c.Request().Context(), &req.Model, &req.Provider); err != nil {
		return handleError(c, err)
	}

	ctx, providerType := ModelCtx(c)
	requestID := c.Request().Header.Get("X-Request-ID")

	resp, err := h.provider.Embeddings(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	h.logUsage(resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, "/v1/embeddings", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

// Batches handles POST /v1/batches.
//
// OpenAI-compatible fields are accepted (`input_file_id`, `endpoint`, `completion_window`, `metadata`).
// Inline `requests` are also accepted for providers with native inline batch support (for example Anthropic).
//
// @Summary      Create a native provider batch
// @Tags         batch
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.BatchRequest  true  "Batch request"
// @Success      200      {object}  core.BatchResponse
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Router       /v1/batches [post]
func (h *Handler) Batches(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemanticEnvelope[*core.BatchRequest](c, core.DecodeBatchRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	requestID := c.Request().Header.Get("X-Request-ID")
	ctx := core.WithRequestID(c.Request().Context(), requestID)

	nativeRouter, ok := h.provider.(core.NativeBatchRoutableProvider)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("batch routing is not supported by the current provider router", nil))
	}

	providerType, err := determineBatchProviderType(h.provider, req)
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntry(c, "batch", providerType)

	upstream, err := nativeRouter.CreateBatch(ctx, providerType, req)
	if err != nil {
		return handleError(c, err)
	}
	if upstream == nil {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty batch response", nil))
	}

	providerBatchID := upstream.ProviderBatchID
	if providerBatchID == "" {
		providerBatchID = upstream.ID
	}
	if providerBatchID == "" {
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider response missing batch id", nil))
	}

	resp := *upstream
	resp.Provider = providerType
	resp.ProviderBatchID = providerBatchID
	resp.ID = "batch_" + uuid.NewString()
	resp.Object = "batch"
	if resp.Endpoint == "" {
		resp.Endpoint = core.NormalizeOperationPath(req.Endpoint)
	}
	if resp.CompletionWindow == "" {
		resp.CompletionWindow = req.CompletionWindow
	}
	if resp.CompletionWindow == "" {
		resp.CompletionWindow = "24h"
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]string{}
	}
	resp.Metadata["provider"] = providerType
	resp.Metadata["provider_batch_id"] = providerBatchID
	if requestID != "" {
		resp.Metadata[batchMetadataRequestIDKey] = requestID
	}

	if h.batchStore != nil {
		if err := h.batchStore.Create(ctx, &resp); err != nil {
			return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to persist batch", err))
		}
	}

	return c.JSON(http.StatusOK, resp)
}

func determineBatchProviderType(provider core.RoutableProvider, req *core.BatchRequest) (string, error) {
	if provider == nil {
		return "", core.NewInvalidRequestError("provider is not configured", nil)
	}

	if strings.TrimSpace(req.InputFileID) != "" {
		if req.Metadata == nil {
			return "", core.NewInvalidRequestError("metadata.provider is required for input_file_id batches", nil)
		}
		providerType := strings.TrimSpace(req.Metadata["provider"])
		if providerType == "" {
			return "", core.NewInvalidRequestError("metadata.provider is required for input_file_id batches", nil)
		}
		return providerType, nil
	}

	if len(req.Requests) == 0 {
		return "", core.NewInvalidRequestError("requests is required and must not be empty", nil)
	}

	var providerType string
	for i, item := range req.Requests {
		selector, err := core.BatchItemModelSelector(req.Endpoint, item)
		if err != nil {
			return "", core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}
		model := selector.QualifiedModel()
		if model == "" {
			return "", core.NewInvalidRequestError(fmt.Sprintf("batch item %d: model is required", i), nil)
		}
		if !provider.Supports(model) {
			return "", core.NewInvalidRequestError("unsupported model: "+model, nil)
		}
		itemProvider := provider.GetProviderType(model)
		if providerType == "" {
			providerType = itemProvider
			continue
		}
		if providerType != itemProvider {
			return "", core.NewInvalidRequestError("native batch supports a single provider per batch; split mixed-provider requests", nil)
		}
	}

	if providerType == "" {
		return "", core.NewInvalidRequestError("unable to resolve provider for batch", nil)
	}
	return providerType, nil
}

func (h *Handler) loadBatch(c *echo.Context, id string) (*core.BatchResponse, error) {
	resp, err := h.batchStore.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return nil, core.NewNotFoundError("batch not found: " + id)
		}
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to load batch", err)
	}
	auditlog.EnrichEntry(c, "batch", resp.Provider)
	return resp, nil
}

// GetBatch handles GET /v1/batches/{id}.
//
// @Summary      Get a batch
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      500  {object}  core.GatewayError
// @Failure      502  {object}  core.GatewayError
// @Router       /v1/batches/{id} [get]
func (h *Handler) GetBatch(c *echo.Context) error {
	batchMeta, err := batchRequestMetadataFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}
	id := ""
	if batchMeta != nil {
		id = strings.TrimSpace(batchMeta.BatchID)
	}
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("batch id is required", nil))
	}

	resp, err := h.loadBatch(c, id)
	if err != nil {
		return handleError(c, err)
	}

	nativeRouter, ok := h.provider.(core.NativeBatchRoutableProvider)
	if !ok {
		return c.JSON(http.StatusOK, resp)
	}
	if resp.Provider == "" || resp.ProviderBatchID == "" {
		return c.JSON(http.StatusOK, resp)
	}

	latest, err := nativeRouter.GetBatch(c.Request().Context(), resp.Provider, resp.ProviderBatchID)
	if err != nil {
		return handleError(c, err)
	}
	if latest != nil {
		mergeStoredBatchFromUpstream(resp, latest)
		if err := h.batchStore.Update(c.Request().Context(), resp); err != nil && !errors.Is(err, batchstore.ErrNotFound) {
			return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to persist refreshed batch", err))
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// ListBatches handles GET /v1/batches.
//
// @Summary      List batches
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        after  query     string  false  "Pagination cursor"
// @Param        limit  query     int     false  "Maximum items to return (1-100, default 20)"
// @Success      200    {object}  core.BatchListResponse
// @Failure      400    {object}  core.GatewayError
// @Failure      401    {object}  core.GatewayError
// @Failure      404    {object}  core.GatewayError
// @Failure      500    {object}  core.GatewayError
// @Router       /v1/batches [get]
func (h *Handler) ListBatches(c *echo.Context) error {
	batchMeta, err := batchRequestMetadataFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}

	limit := 20
	if batchMeta != nil && batchMeta.HasLimit {
		limit = batchMeta.Limit
	}

	after := ""
	if batchMeta != nil {
		after = strings.TrimSpace(batchMeta.After)
	}
	normalizedLimit := limit
	if normalizedLimit <= 0 {
		normalizedLimit = 20
	}
	if normalizedLimit > 100 {
		normalizedLimit = 100
	}

	items, err := h.batchStore.List(c.Request().Context(), normalizedLimit+1, after)
	if err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("after cursor batch not found: "+after))
		}
		return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to list batches", err))
	}
	auditlog.EnrichEntry(c, "batch", "")

	hasMore := len(items) > normalizedLimit
	if hasMore {
		items = items[:normalizedLimit]
	}

	data := make([]core.BatchResponse, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		data = append(data, *item)
	}

	resp := core.BatchListResponse{
		Object:  "list",
		Data:    data,
		HasMore: hasMore,
	}
	if len(data) > 0 {
		resp.FirstID = data[0].ID
		resp.LastID = data[len(data)-1].ID
	}

	return c.JSON(http.StatusOK, resp)
}

// CancelBatch handles POST /v1/batches/{id}/cancel.
//
// @Summary      Cancel a batch
// @Tags         batch
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      500  {object}  core.GatewayError
// @Failure      502  {object}  core.GatewayError
// @Router       /v1/batches/{id}/cancel [post]
func (h *Handler) CancelBatch(c *echo.Context) error {
	batchMeta, err := batchRequestMetadataFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}
	id := ""
	if batchMeta != nil {
		id = strings.TrimSpace(batchMeta.BatchID)
	}
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("batch id is required", nil))
	}

	resp, err := h.loadBatch(c, id)
	if err != nil {
		return handleError(c, err)
	}

	nativeRouter, ok := h.provider.(core.NativeBatchRoutableProvider)
	if !ok || resp.Provider == "" || resp.ProviderBatchID == "" {
		return handleError(c, core.NewInvalidRequestError("native batch cancellation is not available", nil))
	}

	latest, err := nativeRouter.CancelBatch(c.Request().Context(), resp.Provider, resp.ProviderBatchID)
	if err != nil {
		return handleError(c, err)
	}
	if latest != nil {
		mergeStoredBatchFromUpstream(resp, latest)
	}

	if err := h.batchStore.Update(c.Request().Context(), resp); err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("batch not found: "+id))
		}
		return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to cancel batch", err))
	}

	return c.JSON(http.StatusOK, resp)
}

// BatchResults handles GET /v1/batches/{id}/results.
//
// @Summary      Get batch results
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResultsResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      409  {object}  core.GatewayError
// @Failure      500  {object}  core.GatewayError
// @Failure      502  {object}  core.GatewayError
// @Router       /v1/batches/{id}/results [get]
func (h *Handler) BatchResults(c *echo.Context) error {
	batchMeta, err := batchRequestMetadataFromSemanticEnvelope(c)
	if err != nil {
		return handleError(c, err)
	}
	id := ""
	if batchMeta != nil {
		id = strings.TrimSpace(batchMeta.BatchID)
	}
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("batch id is required", nil))
	}

	stored, err := h.loadBatch(c, id)
	if err != nil {
		return handleError(c, err)
	}

	nativeRouter, ok := h.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Provider == "" || stored.ProviderBatchID == "" {
		return c.JSON(http.StatusOK, core.BatchResultsResponse{
			Object:  "list",
			BatchID: stored.ID,
			Data:    stored.Results,
		})
	}

	upstream, err := nativeRouter.GetBatchResults(c.Request().Context(), stored.Provider, stored.ProviderBatchID)
	if err != nil {
		if isNativeBatchResultsPending(err) {
			if latest, getErr := nativeRouter.GetBatch(c.Request().Context(), stored.Provider, stored.ProviderBatchID); getErr == nil && latest != nil {
				mergeStoredBatchFromUpstream(stored, latest)
				if updateErr := h.batchStore.Update(c.Request().Context(), stored); updateErr != nil && !errors.Is(updateErr, batchstore.ErrNotFound) {
					slog.Warn(
						"failed to update batch store after refreshing pending results",
						"batch_id", stored.ID,
						"provider", stored.Provider,
						"provider_batch_id", stored.ProviderBatchID,
						"error", updateErr,
					)
				}
			}
			status := strings.TrimSpace(stored.Status)
			if status == "" {
				status = "in_progress"
			}
			return handleError(c, core.NewInvalidRequestErrorWithStatus(
				http.StatusConflict,
				fmt.Sprintf("batch results are not ready yet (status: %s)", status),
				err,
			))
		}
		return handleError(c, err)
	}
	if upstream == nil {
		return handleError(c, core.NewProviderError(stored.Provider, http.StatusBadGateway, "provider returned empty batch results response", nil))
	}

	result := *upstream
	result.BatchID = stored.ID
	usageLogged := h.logBatchUsageFromBatchResults(stored, &result, strings.TrimSpace(c.Request().Header.Get("X-Request-ID")))
	if len(result.Data) > 0 {
		stored.Results = result.Data
	}
	if len(result.Data) > 0 || usageLogged {
		if updateErr := h.batchStore.Update(c.Request().Context(), stored); updateErr != nil {
			slog.Warn(
				"failed to update batch store after receiving batch results",
				"batch_id", stored.ID,
				"provider", stored.Provider,
				"provider_batch_id", stored.ProviderBatchID,
				"error", updateErr,
			)
		}
	}

	return c.JSON(http.StatusOK, result)
}

func (h *Handler) logBatchUsageFromBatchResults(stored *core.BatchResponse, result *core.BatchResultsResponse, fallbackRequestID string) bool {
	if h.usageLogger == nil || !h.usageLogger.Config().Enabled || stored == nil || result == nil || len(result.Data) == 0 {
		return false
	}
	if stored.Metadata != nil && strings.TrimSpace(stored.Metadata[batchMetadataUsageLoggedKey]) != "" {
		return false
	}

	requestID := strings.TrimSpace(fallbackRequestID)
	if stored.Metadata != nil {
		if originalRequestID := strings.TrimSpace(stored.Metadata[batchMetadataRequestIDKey]); originalRequestID != "" {
			requestID = originalRequestID
		}
	}
	if requestID == "" {
		requestID = "batch:" + stored.ID
	}

	loggedEntries := 0
	inputTotal := 0
	outputTotal := 0
	totalTokens := 0
	var inputCostTotal float64
	var outputCostTotal float64
	var totalCostTotal float64
	hasAnyCost := false

	for _, item := range result.Data {
		if item.StatusCode < http.StatusOK || item.StatusCode >= http.StatusMultipleChoices {
			continue
		}

		payload, ok := asJSONMap(item.Response)
		if !ok {
			continue
		}
		usagePayload, ok := asJSONMap(payload["usage"])
		if !ok {
			continue
		}

		inputTokens, outputTokens, usageTotal, hasUsage := extractTokenTotals(usagePayload)
		if !hasUsage {
			continue
		}

		provider := firstNonEmpty(item.Provider, stored.Provider)
		model := firstNonEmpty(item.Model, stringFromAny(payload["model"]))
		providerID := firstNonEmpty(
			stringFromAny(payload["id"]),
			item.CustomID,
			fmt.Sprintf("%s:%d", firstNonEmpty(stored.ProviderBatchID, stored.ID), item.Index),
		)
		rawUsage := buildBatchUsageRawData(usagePayload, stored, item)

		var pricing *core.ModelPricing
		if h.pricingResolver != nil && model != "" {
			pricing = h.pricingResolver.ResolvePricing(model, provider)
		}

		entry := usage.ExtractFromSSEUsage(
			providerID,
			inputTokens,
			outputTokens,
			usageTotal,
			rawUsage,
			requestID,
			model,
			provider,
			"/v1/batches",
			pricing,
		)
		if entry == nil {
			continue
		}
		entry.ID = deterministicBatchUsageID(stored, item, providerID)

		h.usageLogger.Write(entry)
		loggedEntries++
		inputTotal += inputTokens
		outputTotal += outputTokens
		totalTokens += usageTotal
		if entry.InputCost != nil {
			inputCostTotal += *entry.InputCost
			hasAnyCost = true
		}
		if entry.OutputCost != nil {
			outputCostTotal += *entry.OutputCost
			hasAnyCost = true
		}
		if entry.TotalCost != nil {
			totalCostTotal += *entry.TotalCost
			hasAnyCost = true
		}
	}

	if loggedEntries == 0 {
		return false
	}

	if stored.Metadata == nil {
		stored.Metadata = map[string]string{}
	}
	stored.Metadata[batchMetadataUsageLoggedKey] = strconv.FormatInt(time.Now().Unix(), 10)
	stored.Metadata[batchMetadataRequestIDKey] = requestID

	stored.Usage.InputTokens = inputTotal
	stored.Usage.OutputTokens = outputTotal
	stored.Usage.TotalTokens = totalTokens
	if hasAnyCost {
		stored.Usage.InputCost = &inputCostTotal
		stored.Usage.OutputCost = &outputCostTotal
		stored.Usage.TotalCost = &totalCostTotal
	}

	return true
}

func deterministicBatchUsageID(stored *core.BatchResponse, item core.BatchResultItem, providerID string) string {
	seed := fmt.Sprintf(
		"%s|%s|%d|%s|%s",
		firstNonEmpty(stored.ID, stored.ProviderBatchID),
		firstNonEmpty(stored.ProviderBatchID, stored.ID),
		item.Index,
		item.CustomID,
		providerID,
	)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

func buildBatchUsageRawData(usagePayload map[string]any, stored *core.BatchResponse, item core.BatchResultItem) map[string]any {
	if usagePayload == nil {
		return nil
	}

	raw := make(map[string]any)
	for key, value := range usagePayload {
		switch key {
		case "input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "total_tokens":
			continue
		default:
			raw[key] = value
		}
	}

	if promptDetails, ok := asJSONMap(usagePayload["prompt_tokens_details"]); ok {
		for key, value := range promptDetails {
			raw["prompt_"+key] = value
		}
	}
	if completionDetails, ok := asJSONMap(usagePayload["completion_tokens_details"]); ok {
		for key, value := range completionDetails {
			raw["completion_"+key] = value
		}
	}

	raw["batch_id"] = stored.ID
	raw["provider_batch_id"] = stored.ProviderBatchID
	raw["batch_result_index"] = item.Index
	if item.CustomID != "" {
		raw["batch_custom_id"] = item.CustomID
	}
	if endpoint := strings.TrimSpace(stored.Endpoint); endpoint != "" {
		raw["batch_endpoint"] = endpoint
	}

	return raw
}

func extractTokenTotals(usagePayload map[string]any) (int, int, int, bool) {
	inputTokens, hasInput := readFirstInt(usagePayload, "input_tokens", "prompt_tokens")
	outputTokens, hasOutput := readFirstInt(usagePayload, "output_tokens", "completion_tokens")
	totalTokens, hasTotal := readFirstInt(usagePayload, "total_tokens")
	if !hasTotal && (hasInput || hasOutput) {
		totalTokens = inputTokens + outputTokens
		hasTotal = true
	}

	return inputTokens, outputTokens, totalTokens, hasInput || hasOutput || hasTotal
}

func readFirstInt(values map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if num, ok := intFromAny(value); ok {
			return num, true
		}
	}
	return 0, false
}

func intFromAny(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return intFromInt64(v)
	case uint:
		return intFromUint64(uint64(v))
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return intFromUint64(uint64(v))
	case uint64:
		return intFromUint64(v)
	case float32:
		return intFromFloat64(float64(v))
	case float64:
		return intFromFloat64(v)
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return intFromInt64(i)
		}
		f, err := v.Float64()
		if err == nil {
			return intFromFloat64(f)
		}
		return 0, false
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func intFromInt64(v int64) (int, bool) {
	maxInt := int64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if v < minInt || v > maxInt {
		return 0, false
	}
	return int(v), true
}

func intFromUint64(v uint64) (int, bool) {
	maxInt := uint64(^uint(0) >> 1)
	if v > maxInt {
		return 0, false
	}
	return int(v), true
}

func intFromFloat64(v float64) (int, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	maxInt := float64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if v < minInt || v > maxInt {
		return 0, false
	}
	return int(v), true
}

func asJSONMap(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		return decodeJSONMap(v)
	case []byte:
		return decodeJSONMap(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, false
		}
		return decodeJSONMap([]byte(v))
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return decodeJSONMap(raw)
	}
}

func decodeJSONMap(raw []byte) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mergeStoredBatchFromUpstream(stored, upstream *core.BatchResponse) {
	if stored == nil || upstream == nil {
		return
	}

	stored.Status = upstream.Status
	stored.Endpoint = upstream.Endpoint
	stored.InputFileID = upstream.InputFileID
	stored.CompletionWindow = upstream.CompletionWindow
	stored.RequestCounts = upstream.RequestCounts
	stored.Usage = upstream.Usage
	stored.Results = upstream.Results
	stored.InProgressAt = upstream.InProgressAt
	stored.CompletedAt = upstream.CompletedAt
	stored.FailedAt = upstream.FailedAt
	stored.CancellingAt = upstream.CancellingAt
	stored.CancelledAt = upstream.CancelledAt
	if upstream.Metadata != nil {
		if stored.Metadata == nil {
			stored.Metadata = map[string]string{}
		}
		preservedGatewayMetadata := map[string]string{}
		for _, key := range []string{"provider", "provider_batch_id"} {
			if value, exists := stored.Metadata[key]; exists {
				preservedGatewayMetadata[key] = value
			}
		}
		for key, value := range upstream.Metadata {
			if _, preserve := preservedGatewayMetadata[key]; preserve {
				continue
			}
			stored.Metadata[key] = value
		}
		for key, value := range preservedGatewayMetadata {
			stored.Metadata[key] = value
		}
	}
}

func isNativeBatchResultsPending(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	if gatewayErr.HTTPStatusCode() != http.StatusNotFound {
		return false
	}
	// Some providers return 404 while native results are still being prepared.
	// Extend batchResultsPending404Providers as more provider-specific behaviors are confirmed.
	_, ok := batchResultsPending404Providers[strings.ToLower(strings.TrimSpace(gatewayErr.Provider))]
	return ok
}

// handleError converts gateway errors to appropriate HTTP responses
func handleError(c *echo.Context, err error) error {
	var gatewayErr *core.GatewayError
	if errors.As(err, &gatewayErr) {
		auditlog.EnrichEntryWithError(c, string(gatewayErr.Type), gatewayErr.Message)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	// Fallback for unexpected errors
	auditlog.EnrichEntryWithError(c, "internal_error", "an unexpected error occurred")
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "internal_error",
			"message": "an unexpected error occurred",
		},
	})
}
