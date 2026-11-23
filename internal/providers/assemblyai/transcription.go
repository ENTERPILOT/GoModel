package assemblyai

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// AssemblyAI transcript job states. queued and processing are still running.
const (
	statusCompleted = "completed"
	statusError     = "error"
)

// CreateSpeech reports that this adapter has no text-to-speech. AudioProvider
// groups speech and transcription into one optional capability, so a
// transcription-only provider must still implement this method.
func (p *Provider) CreateSpeech(_ context.Context, _ *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return nil, core.NewInvalidRequestError("assemblyai does not support text-to-speech", nil)
}

// transcriptParams is the POST /v2/transcript body.
type transcriptParams struct {
	AudioURL     string   `json:"audio_url"`
	SpeechModels []string `json:"speech_models"`
	LanguageCode string   `json:"language_code,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	Temperature  *float64 `json:"temperature,omitempty"`
}

// transcript is the part of AssemblyAI's transcript object the adapter reads.
// Times are in milliseconds, audio_duration in seconds.
type transcript struct {
	ID            string           `json:"id"`
	Status        string           `json:"status"`
	Error         string           `json:"error"`
	Text          string           `json:"text"`
	LanguageCode  string           `json:"language_code"`
	AudioDuration float64          `json:"audio_duration"`
	Words         []transcriptSpan `json:"words"`
}

// transcriptSpan is a timed piece of text: a word, or a sentence from
// GET /v2/transcript/{id}/sentences.
type transcriptSpan struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// CreateTranscription serves the OpenAI-compatible transcription request with
// AssemblyAI's pre-recorded API, which is asynchronous: the audio is uploaded,
// a transcript job is created and polled until it finishes, and the result is
// shaped into the requested response_format. The transcript, and with it the
// uploaded audio, is deleted from AssemblyAI before the call returns.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	format, err := transcriptionFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	params, err := newTranscriptParams(req)
	if err != nil {
		return nil, err
	}
	if req.FileReader == nil && len(req.File) == 0 {
		return nil, core.NewInvalidRequestError("file is required", nil)
	}

	// One key serves the whole job: AssemblyAI only lets the project that
	// uploaded a file transcribe it and read the result.
	ctx = context.WithValue(ctx, pinnedKeyContextKey{}, p.keys.NextForContext(ctx))

	params.AudioURL, err = p.upload(ctx, req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode the assemblyai transcript request", err)
	}
	job := &transcript{}
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v2/transcript",
		Headers:  http.Header{"Content-Type": {"application/json"}},
		// A one-shot body is sent once and never retried: a retry after a lost
		// response would create a second job whose id nothing knows to delete.
		RawBodyReader: bytes.NewReader(body),
	}, job); err != nil {
		return nil, err
	}
	if strings.TrimSpace(job.ID) == "" {
		return nil, core.NewProviderError("assemblyai", http.StatusBadGateway, "assemblyai returned a transcript without an id", nil)
	}
	defer p.deleteTranscript(ctx, job.ID)

	job, err = p.awaitTranscript(ctx, job)
	if err != nil {
		return nil, err
	}

	switch format {
	case "srt", "vtt":
		// AssemblyAI renders both subtitle formats itself.
		resp, err := p.client.DoRaw(ctx, llmclient.Request{
			Method:   http.MethodGet,
			Endpoint: transcriptEndpoint(job.ID) + "/" + format,
		})
		if err != nil {
			return nil, err
		}
		return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: resp.Body}, nil
	case "verbose_json":
		withSegments, withWords := timestampGranularities(req)
		var sentences []transcriptSpan
		if withSegments {
			if sentences, err = p.sentences(ctx, job.ID); err != nil {
				return nil, err
			}
		}
		return verboseTranscriptionResponse(job, sentences, withSegments, withWords)
	default:
		return transcriptionResponse(format, job)
	}
}

// transcriptionFormat validates the OpenAI response_format and returns it
// normalized; an unset format is json.
func transcriptionFormat(responseFormat string) (string, error) {
	switch format := strings.ToLower(strings.TrimSpace(responseFormat)); format {
	case "":
		return "json", nil
	case "json", "text", "srt", "verbose_json", "vtt":
		return format, nil
	default:
		return "", core.NewInvalidRequestError("assemblyai transcription supports json, text, srt, verbose_json, or vtt response formats", nil)
	}
}

// newTranscriptParams maps the OpenAI request fields to AssemblyAI's. The
// model id is the speech_models value, and language is the language_code;
// AssemblyAI detects the language itself when none is given.
func newTranscriptParams(req *core.AudioTranscriptionRequest) (*transcriptParams, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	params := &transcriptParams{
		SpeechModels: []string{model},
		LanguageCode: strings.TrimSpace(req.Language),
		Prompt:       strings.TrimSpace(req.Prompt),
	}
	if raw := strings.TrimSpace(req.Temperature); raw != "" {
		// ParseFloat accepts NaN and Inf, which cannot be sent as JSON.
		temperature, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(temperature) || math.IsInf(temperature, 0) {
			return nil, core.NewInvalidRequestError("temperature must be a number", err).WithParam("temperature")
		}
		params.Temperature = &temperature
	}
	return params, nil
}

// timestampGranularities reports which timestamp arrays a verbose_json
// response carries. Like OpenAI, it is segments unless the request asks for
// words, and both when it asks for both.
func timestampGranularities(req *core.AudioTranscriptionRequest) (segments, words bool) {
	for _, granularity := range req.TimestampGranularities {
		switch strings.ToLower(strings.TrimSpace(granularity)) {
		case "segment":
			segments = true
		case "word":
			words = true
		}
	}
	return segments || !words, words
}

func transcriptEndpoint(id string) string {
	return "/v2/transcript/" + url.PathEscape(id)
}

// upload sends the audio bytes to POST /v2/upload and returns the URL the
// transcript job reads them from. Only AssemblyAI's servers can fetch it.
func (p *Provider) upload(ctx context.Context, req *core.AudioTranscriptionRequest) (string, error) {
	upload := llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v2/upload",
		Headers:  http.Header{"Content-Type": {"application/octet-stream"}},
	}
	if req.FileReader != nil {
		upload.RawBodyReader = req.FileReader
	} else {
		upload.RawBody = req.File
	}
	var uploaded struct {
		UploadURL string `json:"upload_url"`
	}
	if err := p.client.Do(ctx, upload, &uploaded); err != nil {
		return "", err
	}
	if strings.TrimSpace(uploaded.UploadURL) == "" {
		return "", core.NewProviderError("assemblyai", http.StatusBadGateway, "assemblyai returned no upload_url", nil)
	}
	return uploaded.UploadURL, nil
}

// awaitTranscript polls the job until it completes or fails, the caller's
// context ends, or jobTimeout passes.
func (p *Provider) awaitTranscript(ctx context.Context, job *transcript) (*transcript, error) {
	waitCtx, cancel := context.WithTimeout(ctx, p.jobTimeout)
	defer cancel()

	wait := p.pollInterval
	for {
		switch job.Status {
		case statusCompleted:
			return job, nil
		case statusError:
			return nil, failedTranscriptError(job.Error)
		}

		timer := time.NewTimer(wait)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return nil, core.NewProviderError("assemblyai", http.StatusBadGateway, "assemblyai transcription was canceled", ctx.Err())
			}
			return nil, core.NewProviderError("assemblyai", http.StatusGatewayTimeout,
				"assemblyai transcription did not finish within "+p.jobTimeout.String(), waitCtx.Err())
		case <-timer.C:
		}
		wait = min(wait+p.pollInterval, 3*p.pollInterval)

		next := &transcript{}
		if err := p.client.Do(waitCtx, llmclient.Request{
			Method:   http.MethodGet,
			Endpoint: transcriptEndpoint(job.ID),
		}, next); err != nil {
			return nil, err
		}
		next.ID = job.ID
		job = next
	}
}

// failedTranscriptError maps a job that ended with status "error". The
// failures AssemblyAI documents come from the audio itself (no audio stream,
// an unsupported file type, audio too short, no speech to detect a language
// from), so the message is returned as an invalid request; a message that
// names an internal error is AssemblyAI's own fault and stays a provider error.
func failedTranscriptError(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "assemblyai transcription failed"
	}
	if strings.Contains(strings.ToLower(message), "internal") {
		return core.NewProviderError("assemblyai", http.StatusBadGateway, message, nil)
	}
	err := core.NewInvalidRequestError(message, nil)
	err.Provider = "assemblyai"
	return err
}

// sentences returns the transcript split into sentences, the closest
// AssemblyAI equivalent of OpenAI's segments.
func (p *Provider) sentences(ctx context.Context, id string) ([]transcriptSpan, error) {
	var resp struct {
		Sentences []transcriptSpan `json:"sentences"`
	}
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: transcriptEndpoint(id) + "/sentences",
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Sentences, nil
}

// deleteTranscript removes the transcript from AssemblyAI, which also deletes
// the audio uploaded for it. AssemblyAI otherwise keeps both for its retention
// period. It runs for failed, timed out, and canceled jobs too, and a failure
// is logged rather than returned: the caller's transcript is already complete.
// The call bypasses the circuit breaker, so a breaker that the job's own
// failures or other requests opened cannot skip the deletion.
func (p *Provider) deleteTranscript(ctx context.Context, id string) {
	ctx = llmclient.WithoutCircuitBreaker(context.WithoutCancel(ctx))
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	if _, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodDelete,
		Endpoint: transcriptEndpoint(id),
	}); err != nil {
		slog.WarnContext(ctx, "assemblyai transcript was not deleted; it stays stored until AssemblyAI's retention removes it",
			"transcript_id", id, "error", err)
	}
}
