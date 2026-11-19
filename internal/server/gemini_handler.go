package server

import (
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/geminiapi"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// GeminiModelMethod handles POST /v1beta/models/{model}:{method}. The Gemini
// API names the model and the method in the last path segment, and a model ID
// may itself contain slashes (openai/gpt-4o), so one wildcard route serves
// every method and dispatches here.
func (h *Handler) GeminiModelMethod(c *echo.Context) error {
	_, method, ok := core.ParseGeminiModelMethodPath(c.Request().URL.Path)
	if !ok {
		return handleRouteNotFound(c)
	}
	switch method {
	case core.GeminiMethodGenerateContent:
		return h.GeminiGenerateContent(c)
	case core.GeminiMethodStreamGenerateContent:
		return h.GeminiStreamGenerateContent(c)
	}
	return handleError(c, core.NewNotFoundError(
		"Gemini API method "+method+" is not supported; use "+core.GeminiMethodGenerateContent+" or "+core.GeminiMethodStreamGenerateContent))
}

// GeminiGenerateContent handles POST /v1beta/models/{model}:generateContent.
//
// It accepts the Gemini API request dialect, translates it to the canonical
// chat request, and runs it through the standard chat-completions pipeline so
// it routes to any configured provider with workflow, guardrail, budget,
// failover, usage, and request log support. See ADR-0017.
//
// @Summary      Generate content (Gemini API)
// @Tags         gemini
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        model    path      string                            true  "Model ID, e.g. gemini-2.5-flash or openai/gpt-4o-mini"
// @Param        request  body      geminiapi.GenerateContentRequest  true  "Gemini generateContent request"
// @Success      200      {object}  geminiapi.GenerateContentResponse
// @Failure      400      {object}  geminiapi.ErrorResponse
// @Failure      401      {object}  geminiapi.ErrorResponse
// @Failure      429      {object}  geminiapi.ErrorResponse
// @Failure      502      {object}  geminiapi.ErrorResponse
// @Router       /v1beta/models/{model}:generateContent [post]
func (h *Handler) GeminiGenerateContent(c *echo.Context) error {
	return h.translatedInference().GenerateContent(c, false)
}

// GeminiStreamGenerateContent handles POST
// /v1beta/models/{model}:streamGenerateContent.
//
// @Summary      Stream generated content (Gemini API)
// @Description  Streams the answer as server-sent events, one GenerateContentResponse per event. alt=sse is required, as the Gemini SDKs send it.
// @Tags         gemini
// @Accept       json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        model    path      string                            true  "Model ID, e.g. gemini-2.5-flash or openai/gpt-4o-mini"
// @Param        alt      query     string                            true  "Must be sse"
// @Param        request  body      geminiapi.GenerateContentRequest  true  "Gemini generateContent request"
// @Success      200      {object}  geminiapi.GenerateContentResponse  "SSE stream of GenerateContentResponse events"
// @Failure      400      {object}  geminiapi.ErrorResponse
// @Failure      401      {object}  geminiapi.ErrorResponse
// @Failure      429      {object}  geminiapi.ErrorResponse
// @Failure      502      {object}  geminiapi.ErrorResponse
// @Router       /v1beta/models/{model}:streamGenerateContent [post]
func (h *Handler) GeminiStreamGenerateContent(c *echo.Context) error {
	return h.translatedInference().GenerateContent(c, true)
}

// ListGeminiModels handles GET /v1beta/models.
//
// @Summary      List available models (Gemini API)
// @Description  Returns the same catalog as GET /v1/models in the Gemini list shape. The whole catalog is one page.
// @Tags         gemini
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  geminiapi.ModelsList
// @Failure      401  {object}  geminiapi.ErrorResponse
// @Failure      502  {object}  geminiapi.ErrorResponse
// @Router       /v1beta/models [get]
func (h *Handler) ListGeminiModels(c *echo.Context) error {
	// Same user-path handling as GET /v1/models, so both apply the same
	// visibility policy.
	if ok, err := applyUserPathHeaderToContext(c); !ok {
		return err
	}
	resp, err := h.visibleModels(c)
	if err != nil {
		return handleError(c, err)
	}
	var models []core.Model
	if resp != nil {
		models = resp.Data
	}
	return c.JSON(http.StatusOK, geminiapi.FromModels(models))
}

// GetGeminiModel handles GET /v1beta/models/{model}.
//
// @Summary      Retrieve a model (Gemini API)
// @Tags         gemini
// @Produce      json
// @Security     BearerAuth
// @Param        model  path      string  true  "Model ID, e.g. gemini-2.5-flash or openai/gpt-4o-mini"
// @Success      200    {object}  geminiapi.ModelInfo
// @Failure      401    {object}  geminiapi.ErrorResponse
// @Failure      404    {object}  geminiapi.ErrorResponse
// @Failure      502    {object}  geminiapi.ErrorResponse
// @Router       /v1beta/models/{model} [get]
func (h *Handler) GetGeminiModel(c *echo.Context) error {
	modelID := retrieveModelID(c)
	if modelID == "" {
		return h.ListGeminiModels(c)
	}
	if ok, err := applyUserPathHeaderToContext(c); !ok {
		return err
	}
	resp, err := h.visibleModels(c)
	if err != nil {
		return handleError(c, err)
	}
	var models []core.Model
	if resp != nil {
		models = resp.Data
	}
	model, ok, err := h.listedModel(c.Request().Context(), models, modelID)
	if err != nil {
		return handleError(c, err)
	}
	if !ok {
		return handleError(c, core.NewModelNotFoundError(modelID))
	}
	return c.JSON(http.StatusOK, geminiapi.FromModel(model))
}

// GenerateContent translates a Gemini generateContent request and dispatches
// it through the shared chat-completions pipeline. The response cache is not
// consulted: it keys on a fixed request path, and this route carries the model
// in its path.
func (s *translatedInferenceService) GenerateContent(c *echo.Context, stream bool) error {
	model, _, _ := core.ParseGeminiModelMethodPath(c.Request().URL.Path)
	// Without alt=sse the Gemini API streams one JSON array instead of
	// events. The Gemini SDKs always ask for SSE, so only that is served.
	if stream && !strings.EqualFold(c.QueryParam("alt"), "sse") {
		return handleError(c, core.NewInvalidRequestError("streamGenerateContent is served as server-sent events only: add alt=sse to the query", nil).WithParam("alt"))
	}

	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	decoded, err := geminiapi.DecodeGenerateContentRequest(body)
	if err != nil {
		return handleError(c, invalidRequestBodyError(c, err))
	}
	req, err := geminiapi.ToChatRequest(decoded, model, stream)
	if err != nil {
		return handleError(c, err)
	}
	opts := geminiapi.ResponseOptions{IncludeThoughts: geminiapi.IncludeThoughts(decoded)}

	ctx := core.WithRequestDialect(promptEditCaptureContext(c, s.logger), core.RequestDialectGeminiGenerateContent)
	ctx, prepared, workflow, err := prepareChatCompletionRequest(s, ctx, req, translatedRequestMeta(c))
	if err != nil {
		// A block, a fail-closed outcome and a short-circuit all belong to the
		// resolved workflow: the request log entry carries it like every other
		// outcome.
		attachPreparedWorkflow(c, prepareContext(c, ctx), workflow)
		s.recordGuardrailOutcomes(c)
		recordPromptPluginRevisions(c, s.logger, req, nil)
		if short := shortCircuitOf(err); short != nil {
			return s.writeChatShortCircuit(c, workflow, req, short,
				func(resp *core.ChatResponse) any { return geminiapi.FromChatResponse(resp, opts) },
				func(stream io.ReadCloser) io.ReadCloser {
					return geminiapi.NewStreamConverter(stream, resolvedModelFromWorkflow(workflow, req.Model), opts)
				})
		}
		return handleError(c, err)
	}
	attachPreparedWorkflow(c, ctx, workflow)
	s.recordGuardrailOutcomes(c)
	recordPromptPluginRevisions(c, s.logger, req, prepared)
	applyPluginRequestHeaders(c)

	return s.dispatchGenerateContent(c, prepared, workflow, opts)
}

func (s *translatedInferenceService) dispatchGenerateContent(c *echo.Context, req *core.ChatRequest, workflow *core.Workflow, opts geminiapi.ResponseOptions) error {
	s.observeLiveProviderAttempts(c, workflow)
	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())
	endpoint := core.UsageEndpoint(c.Request().URL.Path)

	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker,
		rateLimitRouteFromWorkflow(workflow).withFailovers(len(s.inference().FailoverSelectors(workflow))))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()
	ctx = adm.dispatchContext(ctx)

	if req.Stream {
		result, err := s.inference().StreamChatCompletion(ctx, workflow, req)
		if err != nil {
			return handleStreamingDispatchError(c, err)
		}
		if result.Meta.UsedFailover {
			markRequestFailoverUsed(c)
		}
		stream := s.wrapPluginStream(ctx, workflow, chatStreamDialect(false), chatPromptOf(req), result.Stream)
		return s.handleStreamingReadCloser(c, workflow, result.Meta, stream, result.GenerationObserver(), func(stream io.ReadCloser) io.ReadCloser {
			converted := geminiapi.NewStreamConverter(stream, result.Meta.Model, opts)
			return result.WrapDeliveryStream(ctx, converted)
		})
	}

	result, err := s.inference().ExecuteChatCompletion(ctx, workflow, req, requestID, endpoint)
	if err != nil {
		return handleError(c, err)
	}
	enrichRequestLogEntryWithProviderAttempts(c)
	result.Response, err = chatResponsePhase.run(s, c, workflow, req, result.Response)
	if err != nil {
		return handleError(c, err)
	}
	if result.Meta.UsedFailover {
		markRequestFailoverUsed(c)
		requestlog.EnrichEntryWithFailover(c, result.Meta.FailoverModel)
	}
	requestlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)
	notifyChatResponseFeedback(
		c,
		ext.Endpoint(endpoint),
		result.Response,
		result.Meta.Model,
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	applyPluginResponseHeaders(c)
	return c.JSON(http.StatusOK, geminiapi.FromChatResponse(result.Response, opts))
}
