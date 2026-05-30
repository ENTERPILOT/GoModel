package server

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// audioService adapts Echo requests to the model-routed audio provider for the
// OpenAI-compatible /v1/audio/* endpoints. It stays a thin transport layer:
// validate, authorize, enforce budget, route, and proxy the resulting bytes.
type audioService struct {
	provider        core.RoutableProvider
	modelAuthorizer RequestModelAuthorizer
	budgetChecker   BudgetChecker
}

func (s *audioService) router() (core.AudioProvider, error) {
	router, ok := s.provider.(core.AudioProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("audio is not supported by the current provider router", nil)
	}
	return router, nil
}

// CreateSpeech handles POST /v1/audio/speech.
func (s *audioService) CreateSpeech(c *echo.Context) error {
	router, err := s.router()
	if err != nil {
		return handleError(c, err)
	}

	req, err := canonicalJSONRequestFromSemantics[*core.AudioSpeechRequest](c, core.DecodeAudioSpeechRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(req.Input) == "" {
		return handleError(c, core.NewInvalidRequestError("input is required", nil))
	}
	if strings.TrimSpace(req.Voice) == "" {
		return handleError(c, core.NewInvalidRequestError("voice is required", nil))
	}

	ctx, err := s.prepare(c, req.Model, req.Provider)
	if err != nil {
		return handleError(c, err)
	}
	resp, err := router.CreateSpeech(ctx, req)
	if err != nil {
		return handleError(c, err)
	}
	return respondAudio(c, resp)
}

// CreateTranscription handles POST /v1/audio/transcriptions.
func (s *audioService) CreateTranscription(c *echo.Context) error {
	router, err := s.router()
	if err != nil {
		return handleError(c, err)
	}

	req, err := transcriptionRequestFromForm(c)
	if err != nil {
		return handleError(c, err)
	}

	ctx, err := s.prepare(c, req.Model, req.Provider)
	if err != nil {
		return handleError(c, err)
	}
	resp, err := router.CreateTranscription(ctx, req)
	if err != nil {
		return handleError(c, err)
	}
	return respondAudio(c, resp)
}

// selectorResolver maps a requested model selector to the concrete registry
// selector. The production provider (the Router) implements it; when absent, audio
// authorizes on the parsed selector as a fallback.
type selectorResolver interface {
	ResolveModel(core.RequestedModelSelector) (core.ModelSelector, bool, error)
}

// prepare resolves and authorizes the model, enforces budget, and stamps the
// request id, returning the context to dispatch with. Authorization runs on the
// registry-resolved selector so model-override and user-path rules see the same
// concrete provider name as the inference orchestrator.
func (s *audioService) prepare(c *echo.Context, model, providerHint string) (context.Context, error) {
	selector, err := core.ParseModelSelector(model, providerHint)
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}
	if resolver, ok := s.provider.(selectorResolver); ok {
		// Surface resolution failures (registry not ready, malformed selector)
		// instead of authorizing the unresolved selector. The boolean is "did the
		// selector change", not a found flag — on no change resolved already
		// equals the normalized selector, so it is always safe to adopt.
		resolved, _, resolveErr := resolver.ResolveModel(core.NewRequestedModelSelector(model, providerHint))
		if resolveErr != nil {
			return nil, resolveErr
		}
		selector = resolved
	}
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), selector); err != nil {
			return nil, err
		}
	}
	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return nil, err
	}
	auditlog.EnrichEntry(c, selector.Model, "")

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	return ctx, nil
}

func transcriptionRequestFromForm(c *echo.Context) (*core.AudioTranscriptionRequest, error) {
	model := strings.TrimSpace(c.FormValue("model"))
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return nil, core.NewInvalidRequestError("file is required", err)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to open uploaded file", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to read uploaded file", err)
	}

	// Accept both the canonical bracketed key and the unbracketed variant some
	// clients send; the adapter always forwards the bracketed form upstream.
	var granularities []string
	if form, err := c.MultipartForm(); err == nil && form != nil {
		granularities = form.Value["timestamp_granularities[]"]
		if len(granularities) == 0 {
			granularities = form.Value["timestamp_granularities"]
		}
	}

	return &core.AudioTranscriptionRequest{
		Model:                  model,
		Filename:               fileHeader.Filename,
		File:                   data,
		Language:               strings.TrimSpace(c.FormValue("language")),
		Prompt:                 c.FormValue("prompt"),
		ResponseFormat:         strings.TrimSpace(c.FormValue("response_format")),
		Temperature:            strings.TrimSpace(c.FormValue("temperature")),
		TimestampGranularities: granularities,
		Provider:               strings.TrimSpace(c.FormValue("provider")),
	}, nil
}

func respondAudio(c *echo.Context, resp *core.AudioResponse) error {
	if resp == nil {
		return handleError(c, core.NewProviderError("", http.StatusBadGateway, "provider returned empty audio response", nil))
	}
	contentType := strings.TrimSpace(resp.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return c.Blob(http.StatusOK, contentType, resp.Data)
}
