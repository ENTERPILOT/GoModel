package server

import (
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/internal/usage"
)

// maxCapturedAudioResponseBytes bounds how much of a relayed audio body is kept
// in memory for audit capture. It matches the ceiling the audit store embeds
// audio at, so nothing an audit entry would have kept is dropped, while a
// runaway upstream can no longer grow the process without bound (the buffered
// path holds the whole body today). Usage accounting does not share this
// buffer: it measures the relay as it passes (see audioUsageSink), so a
// response past the cap is still billed.
const maxCapturedAudioResponseBytes = 8 * 1024 * 1024

// audioUsageExtractor derives the usage entry for a buffered audio call from the
// complete payload the client received.
type audioUsageExtractor func(data []byte, pricing *core.ModelPricing) *usage.UsageEntry

// audioUsageSink derives it for a relayed one, accumulating what pricing needs
// as the body streams past — a running duration, or the terminal usage event —
// and never the body itself. Measuring a copy of the body instead would have to
// cap that copy, and a capped copy silently stops billing a long response.
type audioUsageSink interface {
	io.Writer
	Entry(pricing *core.ModelPricing) *usage.UsageEntry
}

// audioUsageAccounting prices an audio call from whichever form its body took.
type audioUsageAccounting struct {
	extract       audioUsageExtractor
	newStreamSink func() audioUsageSink
}

// audioStreamSink pairs a bounded accumulator with the function pricing what it
// collected.
type audioStreamSink struct {
	io.Writer
	entry func(*core.ModelPricing) *usage.UsageEntry
}

func (s audioStreamSink) Entry(pricing *core.ModelPricing) *usage.UsageEntry {
	return s.entry(pricing)
}

// audioService adapts Echo requests to the model-routed audio provider for the
// OpenAI-compatible /v1/audio/* endpoints. It stays a thin transport layer:
// validate, authorize, enforce budget, route, and proxy the resulting bytes.
type audioService struct {
	modelCallService
	// logBodies and logAudioBodies mirror the audit logger config. Audio
	// endpoints are not ingress-managed, so the audit middleware cannot capture
	// their (binary/multipart) bodies; the service captures them here instead.
	// logBodies is the master switch: audio bodies are only captured when it is
	// on. logAudioBodies then decides whether the audio bytes are stored as
	// base64 (playable) or as a lightweight placeholder.
	logBodies      bool
	logAudioBodies bool
}

func (s *audioService) router() (core.AudioProvider, error) {
	router, ok := s.provider.(core.AudioProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("audio is not supported by the current provider router", nil)
	}
	return router, nil
}

func (s *audioService) translationRouter() (core.AudioTranslationProvider, error) {
	router, ok := s.provider.(core.AudioTranslationProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("audio translations are not supported by the current provider router", nil)
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
	// Dispatch on the resolved model: an alias never reaches the provider lookup.
	req.Model, req.Provider = route.selector.Model, route.selector.Provider
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	started := time.Now()
	resp, err := router.CreateSpeech(ctx, req)
	if err != nil {
		return handleError(c, err)
	}
	format := speechResponseFormat(req, resp)
	return s.finishAudio(c, route, resp, started, audioUsageAccounting{
		extract: func(data []byte, pricing *core.ModelPricing) *usage.UsageEntry {
			return usage.ExtractFromSpeechRequest(req.Input, data, format, route.requestID, route.model, route.providerType, pricing)
		},
		newStreamSink: func() audioUsageSink {
			meter := usage.NewSpeechDurationMeter(format)
			return audioStreamSink{Writer: meter, entry: func(pricing *core.ModelPricing) *usage.UsageEntry {
				return usage.ExtractFromStreamedSpeechRequest(req.Input, meter, format, route.requestID, route.model, route.providerType, pricing)
			}}
		},
	})
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
	return s.createAudioTranscription(c, false)
}

// CreateTranslation handles POST /v1/audio/translations.
func (s *audioService) CreateTranslation(c *echo.Context) error {
	return s.createAudioTranscription(c, true)
}

func (s *audioService) createAudioTranscription(c *echo.Context, translation bool) error {
	var call func(context.Context, *core.AudioTranscriptionRequest) (*core.AudioResponse, error)
	if translation {
		router, err := s.translationRouter()
		if err != nil {
			return handleError(c, err)
		}
		call = router.CreateTranslation
	} else {
		router, err := s.router()
		if err != nil {
			return handleError(c, err)
		}
		call = router.CreateTranscription
	}

	req, err := audioTranscriptionRequestFromForm(c, !translation)
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
	// Dispatch on the resolved model: an alias never reaches the provider lookup.
	req.Model, req.Provider = route.selector.Model, route.selector.Provider
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	started := time.Now()
	resp, err := call(ctx, req)
	if err != nil {
		return handleError(c, err)
	}
	// The uploaded audio backs duration pricing when the provider reports no
	// usage at all (whisper text/srt/vtt, Groq, ElevenLabs, every translation).
	priceTranscript := func(body []byte, pricing *core.ModelPricing) *usage.UsageEntry {
		if translation {
			return usage.ExtractFromTranslationResponse(body, req.File, route.requestID, route.model, route.providerType, pricing)
		}
		return usage.ExtractFromTranscriptionResponse(body, req.File, route.requestID, route.model, route.providerType, pricing)
	}
	return s.finishAudio(c, route, resp, started, audioUsageAccounting{
		extract: func(data []byte, pricing *core.ModelPricing) *usage.UsageEntry {
			// A transcript relayed by a provider that ignored stream=true is one
			// JSON object; TranscriptUsageBody also recovers the usage event from
			// a body that turned out to be server-sent events.
			return priceTranscript(usage.TranscriptUsageBody(data), pricing)
		},
		newStreamSink: func() audioUsageSink {
			// A relayed transcript reports its usage in the terminal
			// transcript.text.done event, which the collector keeps while
			// discarding the transcript, so a streamed call is priced from the
			// provider's own numbers however long it runs.
			collector := usage.NewTranscriptUsageCollector(maxCapturedAudioResponseBytes)
			return audioStreamSink{Writer: collector, entry: func(pricing *core.ModelPricing) *usage.UsageEntry {
				return priceTranscript(collector.UsageBody(), pricing)
			}}
		},
	})
}

func audioTranscriptionRequestFromForm(c *echo.Context, includeTranscriptionFields bool) (*core.AudioTranscriptionRequest, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid multipart form", err)
	}
	model := strings.TrimSpace(c.FormValue("model"))
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}

	if !includeTranscriptionFields && form != nil {
		for _, field := range []string{"language", "timestamp_granularities", "timestamp_granularities[]"} {
			if _, present := form.Value[field]; present {
				return nil, core.NewInvalidRequestError(field+" is not supported for audio translations", nil)
			}
		}
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

	var granularities []string
	var language string
	if includeTranscriptionFields {
		language = strings.TrimSpace(c.FormValue("language"))
		// Accept both the canonical bracketed key and the unbracketed variant some
		// clients send; the adapter always forwards the bracketed form upstream.
		if form != nil {
			granularities = form.Value["timestamp_granularities[]"]
			if len(granularities) == 0 {
				granularities = form.Value["timestamp_granularities"]
			}
		}
	}

	return &core.AudioTranscriptionRequest{
		Model:                  model,
		Filename:               fileHeader.Filename,
		FileContentType:        fileHeader.Header.Get("Content-Type"),
		File:                   data,
		Language:               language,
		Prompt:                 c.FormValue("prompt"),
		ResponseFormat:         strings.TrimSpace(c.FormValue("response_format")),
		Temperature:            strings.TrimSpace(c.FormValue("temperature")),
		TimestampGranularities: granularities,
		Fields:                 passthroughFormFields(form),
		Provider:               strings.TrimSpace(c.FormValue("provider")),
	}, nil
}

// passthroughFormFields collects the form values the gateway does not consume
// itself so they reach the provider unchanged (ADR-0011 rule 1). Names are
// sorted for a deterministic upstream body; values sharing a name keep their
// request order.
func passthroughFormFields(form *multipart.Form) []core.FormField {
	if form == nil {
		return nil
	}
	names := make([]string, 0, len(form.Value))
	for name := range form.Value {
		if !core.ReservedAudioTranscriptionFormFields[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	fields := make([]core.FormField, 0, len(names))
	for _, name := range names {
		for _, value := range form.Value[name] {
			fields = append(fields, core.FormField{Name: name, Value: value})
		}
	}
	return fields
}

// finishAudio delivers the provider response and records usage for it. A body
// the provider is still producing is relayed as it arrives; a complete one keeps
// the single-blob path.
func (s *audioService) finishAudio(c *echo.Context, route modelCallRoute, resp *core.AudioResponse, started time.Time, accounting audioUsageAccounting) error {
	if resp == nil {
		return s.respondAudio(c, route.providerName, resp) // emits the 502 guard; no usage for a failed call
	}
	if resp.Stream != nil {
		return s.relayAudioStream(c, route, resp, started, accounting.newStreamSink())
	}
	ctx := c.Request().Context()
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return accounting.extract(resp.Data, pricing)
	})
	if err := waitForModelSlowdownFactor(ctx, route.slowdown, time.Since(started)); err != nil {
		return handleError(c, err)
	}
	return s.respondAudio(c, route.providerName, resp)
}

// relayAudioStream forwards a provider audio body to the client chunk by chunk,
// so time-to-first-byte tracks the provider instead of the whole generation:
// synthesized speech and a stream=true transcript are both produced
// incrementally, and buffering them costs the client every second of the
// generation before its first byte. The relayed bytes are teed to the usage sink,
// which measures them as they go, and into a bounded buffer for audit capture.
func (s *audioService) relayAudioStream(c *echo.Context, route modelCallRoute, resp *core.AudioResponse, started time.Time, sink audioUsageSink) error {
	ctx := c.Request().Context()
	// A slowdown factor paces the relay rather than delaying its start, so an
	// artificially slowed model still streams (see streaming.NewSlowdownStream).
	stream := streaming.NewSlowdownStream(ctx, resp.Stream, route.slowdown, started)
	defer func() { _ = stream.Close() }()

	contentType := strings.TrimSpace(resp.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := c.Response().Header()
	header.Set("Content-Type", contentType)
	if isAudioEventStream(contentType) {
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		auditlog.EnrichEntryWithStream(c, true)
	}
	c.Response().WriteHeader(http.StatusOK)

	capture := newCappedCaptureBuffer(maxCapturedAudioResponseBytes)
	flushErr := flushStream(c.Response(), io.TeeReader(stream, io.MultiWriter(sink, capture)))
	// The provider produced (and billed) whatever reached the gateway, so usage
	// is recorded even when the client went away mid-relay.
	s.logUsage(ctx, route, sink.Entry)
	data, _ := capture.Captured()
	s.captureAudioResponseBody(c, contentType, data)
	if flushErr != nil {
		errorType := classifyStreamError(ctx, flushErr)
		auditlog.EnrichEntryWithError(c, errorType, flushErr.Error(), "")
		slog.Warn("audio stream terminated abnormally",
			"error", flushErr,
			"error_type", errorType,
			"path", c.Request().URL.Path,
			"request_id", route.requestID,
		)
	}
	return nil
}

// isAudioEventStream reports whether a relayed body is a server-sent event
// transcript rather than binary audio.
func isAudioEventStream(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

func (s *audioService) respondAudio(c *echo.Context, providerName string, resp *core.AudioResponse) error {
	if resp == nil {
		return handleError(c, core.NewProviderError(providerName, http.StatusBadGateway,
			"provider "+providerName+" returned empty audio response", nil))
	}
	contentType := strings.TrimSpace(resp.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	s.captureAudioResponseBody(c, contentType, resp.Data)
	return c.Blob(http.StatusOK, contentType, resp.Data)
}

// captureAudioResponseBody records the returned payload on the live audit entry.
// The audit middleware cannot: it coerces bodies to UTF-8, which would corrupt
// binary audio, and it skips a response that announced itself as a stream.
// LogBodies is the master switch; LogAudioBodies only decides whether audio
// bytes are embedded as base64 for playback or recorded as a lightweight
// placeholder. A relayed transcript is text, so it is stored as the middleware
// would have, under the same size ceiling.
func (s *audioService) captureAudioResponseBody(c *echo.Context, contentType string, data []byte) {
	if !s.logBodies {
		return
	}
	switch {
	case auditlog.IsAudioContentType(contentType):
		auditlog.EnrichEntryWithResponseBody(c, auditlog.BuildAudioResponseBody(contentType, data, s.logAudioBodies))
	case isAudioEventStream(contentType) && len(data) > 0 && len(data) <= auditlog.MaxBodyCapture:
		auditlog.EnrichEntryWithResponseBody(c, auditlog.CaptureLoggedBody(data))
	}
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
	// Forwarded fields are arbitrary client input and may carry provider-native
	// credentials, so the audit entry records only which ones were passed
	// through, never their values.
	if names := forwardedFieldNames(req.Fields); len(names) > 0 {
		meta["forwarded_fields"] = names
	}
	return meta
}

// forwardedFieldNames lists the distinct passthrough field names in request
// order, for the audit metadata.
func forwardedFieldNames(fields []core.FormField) []string {
	if len(fields) == 0 {
		return nil
	}
	names := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, ok := seen[field.Name]; ok {
			continue
		}
		seen[field.Name] = struct{}{}
		names = append(names, field.Name)
	}
	return names
}
