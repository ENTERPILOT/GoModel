package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// audioService adapts Echo requests to the model-routed audio provider for the
// OpenAI-compatible /v1/audio/* endpoints. It stays a thin transport layer:
// validate, authorize, enforce budget, route, and proxy the resulting bytes.
type audioService struct {
	provider        core.RoutableProvider
	modelAuthorizer RequestModelAuthorizer
	budgetChecker   BudgetChecker
	rateLimiter     RateLimiter
	// logBodies and logAudioBodies mirror the audit logger config. Audio
	// endpoints are not ingress-managed, so the audit middleware cannot capture
	// their (binary/multipart) bodies; the service captures them here instead.
	// logBodies is the master switch: audio bodies are only captured when it is
	// on. logAudioBodies then decides whether the audio bytes are stored as
	// base64 (playable) or as a lightweight placeholder.
	logBodies      bool
	logAudioBodies bool
	// usageLogger and pricingResolver record per-call usage. Audio providers
	// return opaque bytes, so usage is derived locally: input characters for
	// speech, and the optional usage object parsed from a transcription response.
	usageLogger     usage.LoggerInterface
	pricingResolver usage.PricingResolver
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

	if s.logBodies && s.logAudioBodies {
		auditlog.EnrichEntryWithRequestBody(c, audioSpeechAuditInput(req))
	}

	ctx, route, err := s.prepare(c, req.Model, req.Provider)
	if err != nil {
		return handleError(c, err)
	}
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	resp, err := router.CreateSpeech(ctx, req)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return s.respondAudio(c, resp) // emits the 502 guard; no usage for a failed call
	}
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromSpeechRequest(req.Input, resp.Data, speechResponseFormat(req, resp), route.requestID, route.model, route.providerType, pricing)
	})
	return s.respondAudio(c, resp)
}

// speechResponseFormat resolves the codec of the synthesized audio so usage can
// price models billed by output duration. The response Content-Type describes
// the bytes actually returned, so it is authoritative when it names an audio
// media type; this avoids charging a per-second rate against bytes whose real
// format differs from the requested one (e.g. response_format=pcm but the
// provider returns mp3). It falls back to the requested response_format and
// finally OpenAI's mp3 default.
func speechResponseFormat(req *core.AudioSpeechRequest, resp *core.AudioResponse) string {
	if resp != nil && auditlog.IsAudioContentType(resp.ContentType) {
		return resp.ContentType
	}
	if f := strings.TrimSpace(req.ResponseFormat); f != "" {
		return f
	}
	return "mp3"
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

	// LogBodies is the master switch: when on, the upload metadata is always
	// recorded; LogAudioBodies additionally embeds the raw audio as base64 for
	// playback, otherwise the entry keeps a metadata-only placeholder.
	if s.logBodies {
		auditlog.EnrichEntryWithRequestBody(c, auditlog.BuildAudioUploadBody(
			audioUploadContentType(req), req.File, s.logAudioBodies, audioTranscriptionAuditInput(req)))
	}

	ctx, route, err := s.prepare(c, req.Model, req.Provider)
	if err != nil {
		return handleError(c, err)
	}
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	resp, err := router.CreateTranscription(ctx, req)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return s.respondAudio(c, resp) // emits the 502 guard before resp.Data is read
	}
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromTranscriptionResponse(resp.Data, route.requestID, route.model, route.providerType, pricing)
	})
	return s.respondAudio(c, resp)
}

// selectorResolver maps a requested model selector to the concrete registry
// selector. The production provider (the Router) implements it; when absent, audio
// authorizes on the parsed selector as a fallback.
type selectorResolver interface {
	ResolveModel(core.RequestedModelSelector) (core.ModelSelector, bool, error)
}

// audioRoute carries the resolved routing identity for a single audio call,
// used to label its usage entry the same way the inference orchestrator does.
type audioRoute struct {
	model        string
	providerType string
	providerName string
	requestID    string
}

// prepare resolves and authorizes the model, enforces budget, and stamps the
// request id, returning the context to dispatch with and the resolved route.
// Authorization runs on the registry-resolved selector so model-override and
// user-path rules see the same concrete provider name as the inference orchestrator.
func (s *audioService) prepare(c *echo.Context, model, providerHint string) (context.Context, audioRoute, error) {
	selector, err := core.ParseModelSelector(model, providerHint)
	if err != nil {
		return nil, audioRoute{}, core.NewInvalidRequestError(err.Error(), err)
	}
	if resolver, ok := s.provider.(selectorResolver); ok {
		// Surface resolution failures (registry not ready, malformed selector)
		// instead of authorizing the unresolved selector. The boolean is "did the
		// selector change", not a found flag — on no change resolved already
		// equals the normalized selector, so it is always safe to adopt.
		resolved, _, resolveErr := resolver.ResolveModel(core.NewRequestedModelSelector(model, providerHint))
		if resolveErr != nil {
			return nil, audioRoute{}, resolveErr
		}
		selector = resolved
	}
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), selector); err != nil {
			return nil, audioRoute{}, err
		}
	}
	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return nil, audioRoute{}, err
	}
	auditlog.EnrichEntry(c, selector.Model, "")

	ctx, requestID := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	return ctx, s.routeFor(selector, requestID), nil
}

// routeFor maps a resolved selector to its canonical provider type and concrete
// instance name. The name falls back to the selector's provider when the router
// exposes no name resolver.
func (s *audioService) routeFor(selector core.ModelSelector, requestID string) audioRoute {
	qualified := selector.QualifiedModel()
	route := audioRoute{
		model:        selector.Model,
		providerType: s.provider.GetProviderType(qualified),
		providerName: selector.Provider,
		requestID:    requestID,
	}
	if resolver, ok := s.provider.(core.ProviderNameResolver); ok {
		if name := resolver.GetProviderName(qualified); name != "" {
			route.providerName = name
		}
	}
	return route
}

// logUsage records one usage entry for an audio call when usage tracking is on.
// It mirrors the inference orchestrator: resolve pricing, extract the entry, then
// stamp the concrete provider name and user path before the non-blocking write.
func (s *audioService) logUsage(ctx context.Context, route audioRoute, extract func(*core.ModelPricing) *usage.UsageEntry) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled {
		return
	}
	var pricing *core.ModelPricing
	if s.pricingResolver != nil {
		pricingProvider := route.providerName
		if pricingProvider == "" {
			pricingProvider = route.providerType
		}
		pricing = s.pricingResolver.ResolvePricing(route.model, pricingProvider)
	}
	entry := extract(pricing)
	if entry == nil {
		return
	}
	entry.ProviderName = strings.TrimSpace(route.providerName)
	entry.UserPath = core.UserPathFromContext(ctx)
	entry.Labels = core.RequestLabelsFromContext(ctx)
	s.usageLogger.Write(entry)
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
		FileContentType:        fileHeader.Header.Get("Content-Type"),
		File:                   data,
		Language:               strings.TrimSpace(c.FormValue("language")),
		Prompt:                 c.FormValue("prompt"),
		ResponseFormat:         strings.TrimSpace(c.FormValue("response_format")),
		Temperature:            strings.TrimSpace(c.FormValue("temperature")),
		TimestampGranularities: granularities,
		Provider:               strings.TrimSpace(c.FormValue("provider")),
	}, nil
}

func (s *audioService) respondAudio(c *echo.Context, resp *core.AudioResponse) error {
	if resp == nil {
		return handleError(c, core.NewProviderError("", http.StatusBadGateway, "provider returned empty audio response", nil))
	}
	contentType := strings.TrimSpace(resp.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Audio output is binary; the audit middleware skips audio Content-Types so
	// it never corrupts the bytes via UTF-8 coercion. Capture it here instead.
	// Body logging is the master switch (LogBodies); LogAudioBodies only decides
	// whether the bytes are embedded as base64 for playback or recorded as a
	// lightweight placeholder.
	if auditlog.IsAudioContentType(contentType) && s.logBodies {
		auditlog.EnrichEntryWithResponseBody(c, auditlog.BuildAudioResponseBody(contentType, resp.Data, s.logAudioBodies))
	}

	return c.Blob(http.StatusOK, contentType, resp.Data)
}

// audioSpeechAuditInput builds the audit request body for a text-to-speech
// request: the user-facing synthesis parameters, never routing metadata.
func audioSpeechAuditInput(req *core.AudioSpeechRequest) map[string]any {
	input := map[string]any{
		"model": req.Model,
		"input": req.Input,
		"voice": req.Voice,
	}
	if req.ResponseFormat != "" {
		input["response_format"] = req.ResponseFormat
	}
	if req.Speed != 0 {
		input["speed"] = req.Speed
	}
	if req.Instructions != "" {
		input["instructions"] = req.Instructions
	}
	return input
}

// audioUploadContentType resolves a playable audio MIME type for a transcription
// upload: the client-declared part Content-Type when it is an audio type,
// otherwise a best-effort guess from the filename extension (defaulting to mp3).
func audioUploadContentType(req *core.AudioTranscriptionRequest) string {
	// Strip any MIME parameters (e.g. "audio/webm; codecs=opus") so the stored
	// type is a bare media type the dashboard can use directly in a data: URL.
	if ct := strings.TrimSpace(req.FileContentType); ct != "" {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
		if auditlog.IsAudioContentType(mediaType) {
			return mediaType
		}
	}
	switch strings.ToLower(filepath.Ext(req.Filename)) {
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".m4a", ".mp4", ".m4b":
		return "audio/mp4"
	case ".webm":
		return "audio/webm"
	case ".aac":
		return "audio/aac"
	default:
		return "audio/mpeg"
	}
}

// audioTranscriptionAuditInput builds the metadata attached to a logged
// transcription request (model and upload parameters). The uploaded audio
// itself is embedded separately via BuildAudioUploadBody.
func audioTranscriptionAuditInput(req *core.AudioTranscriptionRequest) map[string]any {
	meta := map[string]any{
		"model":      req.Model,
		"filename":   req.Filename,
		"file_bytes": len(req.File),
	}
	if req.Language != "" {
		meta["language"] = req.Language
	}
	if req.Prompt != "" {
		meta["prompt"] = req.Prompt
	}
	if req.ResponseFormat != "" {
		meta["response_format"] = req.ResponseFormat
	}
	if req.Temperature != "" {
		meta["temperature"] = req.Temperature
	}
	if len(req.TimestampGranularities) > 0 {
		meta["timestamp_granularities"] = req.TimestampGranularities
	}
	return meta
}
