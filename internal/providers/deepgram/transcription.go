package deepgram

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// contractListenParams are /listen options that change what Deepgram returns
// into something this adapter cannot shape into an OpenAI transcript: a
// callback answers with a request id and delivers the transcript elsewhere,
// and multichannel splits it across channels.
var contractListenParams = map[string]bool{
	"callback":        true,
	"callback_method": true,
	"multichannel":    true,
}

// listenQuery builds the /listen query string. Deepgram takes every option as
// a query parameter, so form fields the gateway does not consume itself travel
// there verbatim (ADR-0011 rule 1) and reach Deepgram's own features
// (diarize, keyterm, redact, ...). The parameters the gateway derives from the
// OpenAI fields are set last and win.
func listenQuery(req *core.AudioTranscriptionRequest, format string) (url.Values, error) {
	query := url.Values{}
	for _, field := range req.Fields {
		name := strings.TrimSpace(field.Name)
		switch {
		case name == "" || core.ReservedAudioTranscriptionFormFields[name]:
		case name == "stream":
			// OpenAI's incremental transcript; Deepgram's pre-recorded API
			// answers with one complete body.
			if value := strings.ToLower(strings.TrimSpace(field.Value)); value == "true" || value == "1" {
				return nil, core.NewInvalidRequestError("deepgram transcription does not support stream", nil).WithParam("stream")
			}
		case contractListenParams[name]:
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("deepgram transcription does not support %s on this endpoint", name), nil).WithParam(name)
		default:
			query.Add(name, field.Value)
		}
	}

	query.Set("model", strings.TrimSpace(req.Model))
	if language := strings.TrimSpace(req.Language); language != "" {
		query.Set("language", language)
		query.Del("detect_language")
	} else if !query.Has("detect_language") {
		// OpenAI detects the language when none is given; Deepgram would
		// assume English.
		query.Set("detect_language", "true")
	}
	if !query.Has("smart_format") {
		// Punctuation, capitalization and number formatting, which OpenAI
		// transcripts carry and Deepgram leaves out by default.
		query.Set("smart_format", "true")
	}
	// Utterances are the timed phrases segments and caption cues are built from.
	switch format {
	case "verbose_json", "srt", "vtt":
		query.Set("utterances", "true")
	}
	return query, nil
}

// uploadContentType labels the audio Deepgram receives as the request body.
// Deepgram detects the format from the bytes, so only a media type is passed
// on, and anything else becomes the generic type Deepgram's own SDK uploads
// with: a JSON type would make it read the body as a {"url": ...} request.
func uploadContentType(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		return contentType
	}
	return "application/octet-stream"
}

type listenWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type listenUtterance struct {
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Transcript string  `json:"transcript"`
}

// listenResponse is the part of Deepgram's pre-recorded result the gateway
// reads. Channels is nil when the body is not a transcription result at all.
type listenResponse struct {
	Metadata struct {
		Duration float64 `json:"duration"`
	} `json:"metadata"`
	Results struct {
		Channels []struct {
			DetectedLanguage string `json:"detected_language"`
			Alternatives     []struct {
				Transcript string       `json:"transcript"`
				Words      []listenWord `json:"words"`
			} `json:"alternatives"`
		} `json:"channels"`
		Utterances []listenUtterance `json:"utterances"`
	} `json:"results"`
}

// CreateTranscription translates the OpenAI-compatible multipart request to
// Deepgram's pre-recorded POST /listen, which takes the audio as the request
// body and every option as a query parameter.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	format := strings.ToLower(strings.TrimSpace(req.ResponseFormat))
	switch format {
	case "", "json", "text", "verbose_json", "srt", "vtt":
	default:
		return nil, core.NewInvalidRequestError("deepgram transcription supports json, text, verbose_json, srt, or vtt response formats", nil).WithParam("response_format")
	}
	if strings.TrimSpace(req.Prompt) != "" {
		return nil, core.NewInvalidRequestError("deepgram transcription does not support prompt; send Deepgram's keyterm field to boost specific terms", nil).WithParam("prompt")
	}
	wantWords, wantSegments, err := timestampGranularities(req.TimestampGranularities)
	if err != nil {
		return nil, err
	}
	query, err := listenQuery(req, format)
	if err != nil {
		return nil, err
	}

	upstream := llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/listen?" + query.Encode(),
		Model:    req.Model,
		Headers:  http.Header{"Content-Type": {uploadContentType(req.FileContentType)}},
	}
	// The buffered upload is replayable, so it keeps the client's retries.
	switch {
	case len(req.File) > 0:
		upstream.RawBody = req.File
	case req.FileReader != nil:
		upstream.RawBodyReader = req.FileReader
	default:
		return nil, core.NewInvalidRequestError("file is required", nil)
	}

	raw, err := p.client.DoRaw(ctx, upstream)
	if err != nil {
		return nil, refineError(err)
	}
	var result listenResponse
	if err := json.Unmarshal(raw.Body, &result); err != nil {
		return nil, core.NewProviderError("deepgram", http.StatusBadGateway, "failed to parse transcription response", err)
	}
	if len(result.Results.Channels) == 0 || len(result.Results.Channels[0].Alternatives) == 0 {
		return nil, core.NewProviderError("deepgram", http.StatusBadGateway, "deepgram returned an unrecognized transcription response", nil)
	}

	body, err := transcriptionBody(format, strings.TrimSpace(req.Language), wantWords, wantSegments, &result)
	if err != nil {
		return nil, core.NewProviderError("deepgram", http.StatusBadGateway, "failed to encode transcription response", err)
	}
	return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: body}, nil
}

// timestampGranularities reads OpenAI's timestamp_granularities, which select
// the timing arrays of a verbose_json transcript. Segments are the default.
func timestampGranularities(granularities []string) (words, segments bool, err error) {
	for _, granularity := range granularities {
		switch strings.ToLower(strings.TrimSpace(granularity)) {
		case "":
		case "word":
			words = true
		case "segment":
			segments = true
		default:
			return false, false, core.NewInvalidRequestError("deepgram transcription supports word or segment timestamp granularities", nil).WithParam("timestamp_granularities")
		}
	}
	if !words {
		segments = true
	}
	return words, segments, nil
}

// transcriptUsage is OpenAI's duration-billed usage object. Reporting it lets
// the gateway meter the audio Deepgram measured instead of probing the upload.
type transcriptUsage struct {
	Type    string  `json:"type"`
	Seconds float64 `json:"seconds"`
}

type transcriptSegment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// transcript is the OpenAI transcription object; verbose_json fills the
// members past Text.
type transcript struct {
	Task     string              `json:"task,omitempty"`
	Language string              `json:"language,omitempty"`
	Duration float64             `json:"duration,omitempty"`
	Text     string              `json:"text"`
	Segments []transcriptSegment `json:"segments,omitempty"`
	Words    []listenWord        `json:"words,omitempty"`
	Usage    *transcriptUsage    `json:"usage,omitempty"`
}

// transcriptionBody shapes Deepgram's result into the OpenAI response_format
// the caller asked for. Deepgram has no caption output of its own; srt and vtt
// are written from the utterances, one cue each, as its captioning guide does.
func transcriptionBody(format, language string, wantWords, wantSegments bool, result *listenResponse) ([]byte, error) {
	channel := result.Results.Channels[0]
	best := channel.Alternatives[0]
	switch format {
	case "text":
		return []byte(best.Transcript), nil
	case "srt", "vtt":
		return captions(format, result.Results.Utterances), nil
	}

	out := transcript{Text: best.Transcript}
	if duration := result.Metadata.Duration; duration > 0 {
		out.Usage = &transcriptUsage{Type: "duration", Seconds: duration}
	}
	if format == "verbose_json" {
		out.Task = "transcribe"
		out.Language = language
		if out.Language == "" {
			out.Language = channel.DetectedLanguage
		}
		out.Duration = result.Metadata.Duration
		if wantWords {
			out.Words = best.Words
		}
		if wantSegments {
			for i, utterance := range result.Results.Utterances {
				out.Segments = append(out.Segments, transcriptSegment{
					ID: i, Start: utterance.Start, End: utterance.End, Text: utterance.Transcript,
				})
			}
		}
	}
	return json.Marshal(out)
}

// captions writes one SubRip or WebVTT cue per utterance. The two formats
// differ in the header, the cue numbering and the millisecond separator.
func captions(format string, utterances []listenUtterance) []byte {
	var b strings.Builder
	separator := ","
	if format == "vtt" {
		b.WriteString("WEBVTT\n\n")
		separator = "."
	}
	for i, utterance := range utterances {
		if format == "srt" {
			fmt.Fprintf(&b, "%d\n", i+1)
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n",
			captionTime(utterance.Start, separator), captionTime(utterance.End, separator), utterance.Transcript)
	}
	return []byte(b.String())
}

// captionTime formats seconds as HH:MM:SS plus milliseconds.
func captionTime(seconds float64, separator string) string {
	ms := int64(seconds*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, separator, ms%1000)
}
