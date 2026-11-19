package speechmatics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// maxErrorBodyBytes bounds how much of a failed job submission is read; the
// body only feeds the error message.
const maxErrorBodyBytes = 64 << 10

// Job statuses reported by GET /jobs/{id}.
const (
	jobDone     = "done"
	jobRejected = "rejected"
	jobDeleted  = "deleted"
	jobExpired  = "expired"
)

type jobConfig struct {
	Type                         string                        `json:"type"`
	TranscriptionConfig          transcriptionConfig           `json:"transcription_config"`
	LanguageIdentificationConfig *languageIdentificationConfig `json:"language_identification_config,omitempty"`
}

type languageIdentificationConfig struct {
	LowConfidenceAction string `json:"low_confidence_action"`
}

type transcriptionConfig struct {
	Model         string   `json:"model"`
	Language      string   `json:"language"`
	LanguageHints []string `json:"language_hints,omitempty"`
}

// newJobConfig maps the OpenAI model and language onto a Speechmatics job.
// Speechmatics requires a language, while OpenAI's is optional: without one the
// job asks for language identification ("auto"). The multilingual models take
// the fixed language "multi" and reject "auto", so a caller's language becomes
// a hint there instead.
func newJobConfig(model, language string) jobConfig {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "zh" {
		// OpenAI clients send ISO 639-1; Speechmatics names Mandarin "cmn".
		language = "cmn"
	}
	job := jobConfig{Type: "transcription", TranscriptionConfig: transcriptionConfig{Model: model, Language: language}}
	cfg := &job.TranscriptionConfig
	switch {
	case multilingualModels[model]:
		cfg.Language = "multi"
		if language != "" && language != "multi" && language != "auto" {
			cfg.LanguageHints = []string{language}
		}
	case language == "" || language == "auto":
		cfg.Language = "auto"
		// Identification rejects a job it is not confident about, which a
		// short clip rarely is. Take its best guess instead, as OpenAI's
		// models do when no language is given.
		job.LanguageIdentificationConfig = &languageIdentificationConfig{LowConfidenceAction: "allow"}
	}
	return job
}

// transcriptionFormat validates the OpenAI response_format. Every OpenAI
// format is served: srt and vtt come from Speechmatics' own subtitles, the
// others are built from its JSON transcript.
func transcriptionFormat(responseFormat string) (string, error) {
	switch format := strings.ToLower(strings.TrimSpace(responseFormat)); format {
	case "":
		return "json", nil
	case "json", "text", "verbose_json", "srt", "vtt":
		return format, nil
	default:
		return "", core.NewInvalidRequestError("speechmatics transcription supports json, text, verbose_json, srt, or vtt response formats", nil)
	}
}

// CreateTranscription runs the OpenAI-compatible request as a Speechmatics
// Batch job: it uploads the audio (POST /jobs), waits for the job to finish
// (GET /jobs/{id}), and fetches the transcript (GET /jobs/{id}/transcript).
// The whole exchange is bounded by the request context and the job timeout; a
// job that is still running when the gateway stops waiting is terminated.
//
// prompt and temperature are style and sampling hints with no Speechmatics
// counterpart, so they are dropped (ADR-0011 rule 4).
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	format, err := transcriptionFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	content := req.FileReader
	if content == nil && len(req.File) > 0 {
		content = bytes.NewReader(req.File)
	}
	if content == nil {
		return nil, core.NewInvalidRequestError("file is required", nil)
	}
	config, err := json.Marshal(newJobConfig(model, req.Language))
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode speechmatics job config", err)
	}

	ctx = context.WithValue(ctx, pinnedKeyContextKey{}, p.keys.NextForContext(ctx))
	jobCtx, cancel := context.WithTimeout(ctx, p.jobTimeout)
	defer cancel()

	body, err := p.runJob(jobCtx, model, format, config, req.Filename, content)
	if err != nil {
		if ctx.Err() == nil && errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return nil, core.NewProviderError(p.name, http.StatusGatewayTimeout,
				"speechmatics transcription did not finish within "+p.jobTimeout.String(), err)
		}
		return nil, err
	}
	return transcriptionResponse(p.name, req, format, body)
}

// runJob returns the finished job's transcript: SRT for the subtitle formats,
// the json-v2 document otherwise.
func (p *Provider) runJob(ctx context.Context, model, format string, config []byte, filename string, content io.Reader) ([]byte, error) {
	jobID, err := p.createJob(ctx, model, config, filename, content)
	if err != nil {
		return nil, err
	}
	if err := p.awaitJob(ctx, model, jobID); err != nil {
		return nil, err
	}

	upstreamFormat := "json-v2"
	if format == "srt" || format == "vtt" {
		upstreamFormat = "srt"
	}
	resp, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: jobEndpoint(jobID) + "/transcript?format=" + upstreamFormat,
		Model:    model,
	})
	if err != nil {
		return nil, refineError(err)
	}
	return resp.Body, nil
}

func jobEndpoint(jobID string) string {
	return "/jobs/" + url.PathEscape(jobID)
}

// createJob uploads the audio and returns the new job's id. Speechmatics
// answers 201, which llmclient's typed calls treat as a failure, so the
// request goes through DoPassthrough and the status is checked here. That
// path also never retries a POST, so a transport failure cannot leave two
// billable jobs behind.
func (p *Provider) createJob(ctx context.Context, model string, config []byte, filename string, content io.Reader) (string, error) {
	body, contentType := jobMultipart(config, filename, content)
	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:        http.MethodPost,
		Endpoint:      "/jobs",
		Model:         model,
		RawBodyReader: body,
		Headers:       http.Header{"Content-Type": {contentType}},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return "", refineError(core.ParseProviderError(p.name, resp.StatusCode, raw, nil))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", core.NewProviderError(p.name, http.StatusBadGateway, "failed to parse speechmatics job response", err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", core.NewProviderError(p.name, http.StatusBadGateway, "speechmatics returned no job id", nil)
	}
	return created.ID, nil
}

// jobMultipart streams the config JSON followed by the audio as data_file.
func jobMultipart(config []byte, filename string, content io.Reader) (io.Reader, string) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "audio"
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer func() { _ = pw.Close() }()

		if err := writer.WriteField("config", string(config)); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to write config field", err))
			return
		}
		part, err := writer.CreateFormFile("data_file", filename)
		if err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to create multipart file field", err))
			return
		}
		if _, err := io.Copy(part, content); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to stream file content", err))
			return
		}
		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to finalize multipart payload", err))
		}
	}()
	return pr, writer.FormDataContentType()
}

type jobDetails struct {
	Job struct {
		Status string `json:"status"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"job"`
}

// awaitJob polls the job until it is done. Speechmatics' cloud holds each
// status request for a few seconds and answers as soon as the job ends, so the
// pause only makes up the gap when an answer came back sooner than that.
func (p *Provider) awaitJob(ctx context.Context, model, jobID string) error {
	interval := p.pollInterval
	for {
		started := time.Now()
		var details jobDetails
		if err := p.client.Do(ctx, llmclient.Request{
			Method:   http.MethodGet,
			Endpoint: jobEndpoint(jobID),
			Model:    model,
		}, &details); err != nil {
			p.abandonJob(ctx, model, jobID)
			return refineError(err)
		}

		switch details.Job.Status {
		case jobDone:
			return nil
		case jobRejected:
			return p.rejectedError(details)
		case jobDeleted, jobExpired:
			return core.NewProviderError(p.name, http.StatusBadGateway,
				"speechmatics transcription job was "+details.Job.Status+" before it finished", nil)
		}

		if err := sleep(ctx, interval-time.Since(started)); err != nil {
			p.abandonJob(ctx, model, jobID)
			return p.interruptedError(err)
		}
		interval = min(interval*2, p.maxPollInterval)
	}
}

// interruptedError reports a wait that the request context ended.
func (p *Provider) interruptedError(err error) error {
	status := http.StatusBadGateway
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	return core.NewProviderError(p.name, status, "speechmatics transcription was interrupted while the job was running", err)
}

// rejectedError reports a job Speechmatics accepted and then could not
// transcribe. The documented causes are in the request (unreadable audio, no
// detectable language, an unsupported language), so it is the caller's error.
func (p *Provider) rejectedError(details jobDetails) error {
	messages := make([]string, 0, len(details.Job.Errors))
	for _, jobErr := range details.Job.Errors {
		if message := strings.TrimSpace(jobErr.Message); message != "" {
			messages = append(messages, message)
		}
	}
	message := "speechmatics rejected the transcription job"
	if len(messages) > 0 {
		message += ": " + strings.Join(messages, "; ")
	}
	gatewayErr := core.NewInvalidRequestError(message, nil)
	gatewayErr.Provider = p.name
	return gatewayErr
}

// abandonJob terminates a job the gateway stopped waiting for, so it does not
// keep running (and billing) for a result nobody will read. It runs on a
// context detached from the request's, which is usually the one that ended.
func (p *Provider) abandonJob(ctx context.Context, model, jobID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	if _, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodDelete,
		Endpoint: jobEndpoint(jobID) + "?force=true",
		Model:    model,
	}); err != nil {
		slog.WarnContext(ctx, "speechmatics: could not terminate an abandoned transcription job",
			"provider", p.name, "job_id", jobID, "error", err)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// refineError improves the message on a *core.GatewayError built from a
// Speechmatics error body. Speechmatics answers {"code": 400, "error": "Job
// rejected", "detail": "..."}; the generic parser keeps only "error", which
// names the failure class and leaves out the detail that explains it.
// Other errors (transport failures, HTML bodies) pass through unchanged.
func refineError(err error) error {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return err
	}
	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(gatewayErr.ResponseBody, &body) != nil || strings.TrimSpace(body.Detail) == "" {
		return err
	}
	refined := *gatewayErr
	refined.Message = strings.TrimSpace(body.Detail)
	if summary := strings.TrimSpace(body.Error); summary != "" {
		refined.Message = summary + ": " + refined.Message
	}
	return &refined
}
