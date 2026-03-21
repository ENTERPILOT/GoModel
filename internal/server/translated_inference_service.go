package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
	"gomodel/internal/streaming"
	"gomodel/internal/usage"
)

// translatedInferenceService owns translated chat/responses/embeddings
// execution so HTTP handlers can stay focused on transport concerns.
type translatedInferenceService struct {
	provider                 core.RoutableProvider
	modelResolver            RequestModelResolver
	translatedRequestPatcher TranslatedRequestPatcher
	logger                   auditlog.LoggerInterface
	usageLogger              usage.LoggerInterface
	pricingResolver          usage.PricingResolver
	responseCache            *responsecache.ResponseCacheMiddleware
	guardrailsHash           string
}

func (s *translatedInferenceService) ChatCompletion(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.ChatRequest](c, core.DecodeChatRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	plan, err := ensureTranslatedRequestPlan(c, s.provider, s.modelResolver, &req.Model, &req.Provider)
	if err != nil {
		return handleError(c, err)
	}

	ctx := c.Request().Context()
	if s.translatedRequestPatcher != nil {
		req, err = s.translatedRequestPatcher.PatchChatRequest(ctx, req)
		if err != nil {
			return handleError(c, err)
		}
	}

	if s.guardrailsHash != "" {
		ctx = core.WithGuardrailsHash(ctx, s.guardrailsHash)
		c.SetRequest(c.Request().WithContext(ctx))
	}

	if s.responseCache != nil && !req.Stream {
		body, marshalErr := marshalRequestBody(req)
		if marshalErr != nil {
			slog.Debug("marshalRequestBody failed", "err", marshalErr)
		} else {
			return s.responseCache.HandleRequest(c, body, func() error {
				return s.dispatchChatCompletion(c, req, plan)
			})
		}
	}

	return s.dispatchChatCompletion(c, req, plan)
}

func (s *translatedInferenceService) dispatchChatCompletion(c *echo.Context, req *core.ChatRequest, plan *core.ExecutionPlan) error {
	ctx := c.Request().Context()
	streamReq, providerType, usageModel := s.resolveProviderAndModelFromPlan(c, plan, req.Model, req)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		return s.handleStreamingResponse(c, usageModel, providerType, func() (io.ReadCloser, error) {
			return s.provider.StreamChatCompletion(ctx, streamReq)
		})
	}

	resp, err := s.provider.ChatCompletion(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	s.logUsage(resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromChatResponse(resp, requestID, providerType, "/v1/chat/completions", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) Responses(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.ResponsesRequest](c, core.DecodeResponsesRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	plan, err := ensureTranslatedRequestPlan(c, s.provider, s.modelResolver, &req.Model, &req.Provider)
	if err != nil {
		return handleError(c, err)
	}

	ctx := c.Request().Context()
	if s.translatedRequestPatcher != nil {
		req, err = s.translatedRequestPatcher.PatchResponsesRequest(ctx, req)
		if err != nil {
			return handleError(c, err)
		}
	}

	if s.guardrailsHash != "" {
		ctx = core.WithGuardrailsHash(ctx, s.guardrailsHash)
		c.SetRequest(c.Request().WithContext(ctx))
	}

	if s.responseCache != nil && !req.Stream {
		body, marshalErr := marshalRequestBody(req)
		if marshalErr != nil {
			slog.Debug("marshalRequestBody failed", "err", marshalErr)
		} else {
			return s.responseCache.HandleRequest(c, body, func() error {
				return s.dispatchResponses(c, req, plan)
			})
		}
	}

	return s.dispatchResponses(c, req, plan)
}

func (s *translatedInferenceService) dispatchResponses(c *echo.Context, req *core.ResponsesRequest, plan *core.ExecutionPlan) error {
	ctx := c.Request().Context()
	_, providerType, usageModel := s.resolveProviderAndModelFromPlan(c, plan, req.Model, nil)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		if s.shouldEnforceReturningUsageData() {
			ctx = core.WithEnforceReturningUsageData(ctx, true)
		}
		return s.handleStreamingResponse(c, usageModel, providerType, func() (io.ReadCloser, error) {
			return s.provider.StreamResponses(ctx, req)
		})
	}

	resp, err := s.provider.Responses(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	s.logUsage(resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromResponsesResponse(resp, requestID, providerType, "/v1/responses", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) Embeddings(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.EmbeddingRequest](c, core.DecodeEmbeddingRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	plan, err := ensureTranslatedRequestPlan(c, s.provider, s.modelResolver, &req.Model, &req.Provider)
	if err != nil {
		return handleError(c, err)
	}

	ctx := c.Request().Context()
	_, providerType, _ := s.resolveProviderAndModelFromPlan(c, plan, req.Model, nil)
	requestID := requestIDFromContextOrHeader(c.Request())

	resp, err := s.provider.Embeddings(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	s.logUsage(resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, "/v1/embeddings", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) handleStreamingResponse(c *echo.Context, model, provider string, streamFn func() (io.ReadCloser, error)) error {
	stream, err := streamFn()
	if err != nil {
		return handleError(c, err)
	}

	auditlog.MarkEntryAsStreaming(c, true)
	auditlog.EnrichEntryWithStream(c, true)

	entry := auditlog.GetStreamEntryFromContext(c)
	streamEntry := auditlog.CreateStreamEntry(entry)
	if streamEntry != nil {
		streamEntry.StatusCode = http.StatusOK
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	endpoint := c.Request().URL.Path
	observers := make([]streaming.Observer, 0, 2)
	if s.logger != nil && s.logger.Config().Enabled && streamEntry != nil {
		observers = append(observers, auditlog.NewStreamLogObserver(s.logger, streamEntry, endpoint))
	}
	if s.usageLogger != nil && s.usageLogger.Config().Enabled {
		observers = append(observers, usage.NewStreamUsageObserver(s.usageLogger, model, provider, requestID, endpoint, s.pricingResolver))
	}
	wrappedStream := streaming.NewObservedSSEStream(stream, observers...)

	defer func() {
		_ = wrappedStream.Close() //nolint:errcheck
	}()

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")

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

func (s *translatedInferenceService) logUsage(model, providerType string, extractFn func(*core.ModelPricing) *usage.UsageEntry) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled {
		return
	}
	var pricing *core.ModelPricing
	if s.pricingResolver != nil {
		pricing = s.pricingResolver.ResolvePricing(model, providerType)
	}
	if entry := extractFn(pricing); entry != nil {
		s.usageLogger.Write(entry)
	}
}

func (s *translatedInferenceService) shouldEnforceReturningUsageData() bool {
	return s.usageLogger != nil && s.usageLogger.Config().EnforceReturningUsageData
}

func (s *translatedInferenceService) resolveProviderAndModelFromPlan(
	c *echo.Context,
	plan *core.ExecutionPlan,
	fallbackModel string,
	req *core.ChatRequest,
) (*core.ChatRequest, string, string) {
	providerType := GetProviderType(c)
	if plan != nil {
		if plannedProviderType := strings.TrimSpace(plan.ProviderType); plannedProviderType != "" {
			providerType = plannedProviderType
		}
	}

	model := resolvedModelFromPlan(plan, fallbackModel)
	if req == nil || !req.Stream || !s.shouldEnforceReturningUsageData() {
		return req, providerType, model
	}

	streamReq := cloneChatRequestForStreamUsage(req)
	if streamReq.StreamOptions == nil {
		streamReq.StreamOptions = &core.StreamOptions{}
	}
	streamReq.StreamOptions.IncludeUsage = true
	return streamReq, providerType, model
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

func cloneChatRequestForStreamUsage(req *core.ChatRequest) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	return &cloned
}

func resolvedModelFromPlan(plan *core.ExecutionPlan, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if plan == nil || plan.Resolution == nil {
		return fallback
	}
	if resolvedModel := strings.TrimSpace(plan.Resolution.ResolvedSelector.Model); resolvedModel != "" {
		return resolvedModel
	}
	return fallback
}

// marshalRequestBody serializes a patched request struct to JSON bytes for cache key computation.
// Returns an error only on marshalling failure; callers bypass cache on error.
func marshalRequestBody(req any) ([]byte, error) {
	return json.Marshal(req)
}
