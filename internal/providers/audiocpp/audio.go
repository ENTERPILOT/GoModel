package audiocpp

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// CreateSpeech forwards an OpenAI-compatible text-to-speech request to
// audio.cpp's POST /v1/audio/speech. The body travels as sent, so the fields
// audio.cpp adds to the OpenAI shape (voice_ref, reference_text, seed,
// max_tokens, options, stream_format, ...) reach it unchanged through
// ExtraFields.
//
// The "voice" a caller sends names an audio.cpp voice preset, cached voice id,
// or voice-library wav rather than an OpenAI voice, since audio.cpp has no
// fixed voice set.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio speech request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, core.NewInvalidRequestError("input is required", nil)
	}

	raw, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v1/audio/speech",
		Model:    req.Model,
		Body:     req,
	})
	if err != nil {
		return nil, err
	}
	return &core.AudioResponse{ContentType: speechContentType(raw), Data: raw.Body}, nil
}

// speechContentType describes the bytes audio.cpp actually returned. Its
// synthesis output is WAV regardless of the requested response_format (a
// frontend build adds MP3), so the upstream header is the only honest source
// and the fallback is wav rather than OpenAI's mp3 default.
func speechContentType(raw *llmclient.Response) string {
	if raw != nil {
		if contentType := strings.TrimSpace(raw.ContentType); contentType != "" {
			return contentType
		}
	}
	return core.SpeechResponseContentType("wav")
}

// audioPathFields are the request fields audio.cpp reads a server-local audio
// path from, in its own resolution order. They arrive as passthrough form
// values because the gateway's typed transcription request has no member for
// them.
var audioPathFields = []string{"audio", "audio_path"}

// CreateTranscription implements speech-to-text against audio.cpp's POST
// /v1/audio/transcriptions, which accepts two request shapes:
//
//   - an OpenAI-style multipart upload, used whenever the caller sent file
//     bytes;
//   - a JSON body naming a path the server itself can read, used when the
//     caller sent an "audio" (or "audio_path") field instead of a file.
//
// The second shape is why the gateway does not insist on a "file" part: the
// audio never leaves the machine audio.cpp runs on, so there is nothing to
// upload. A request carrying neither is still an error, and says so.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	// Checked before dispatch: srt and vtt cannot be shaped from what audio.cpp
	// returns, and finding that out after the model ran wastes the inference.
	switch strings.ToLower(strings.TrimSpace(req.ResponseFormat)) {
	case "", "json", "verbose_json", "text":
	default:
		return nil, core.NewInvalidRequestError("audiocpp transcription supports json, verbose_json, or text response formats", nil)
	}

	content := req.FileReader
	if content == nil && len(req.File) > 0 {
		content = bytes.NewReader(req.File)
	}
	if content == nil {
		return p.transcribeServerPath(ctx, req)
	}
	return p.transcribeUpload(ctx, req, content)
}

func (p *Provider) transcribeUpload(ctx context.Context, req *core.AudioTranscriptionRequest, content io.Reader) (*core.AudioResponse, error) {
	body, contentType := transcriptionMultipart(req, content)
	raw, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:        http.MethodPost,
		Endpoint:      "/v1/audio/transcriptions",
		Model:         req.Model,
		RawBodyReader: body,
		Headers:       http.Header{"Content-Type": {contentType}},
	})
	if err != nil {
		return nil, err
	}
	return transcriptionResponse(req, raw)
}

// transcribeServerPath sends the JSON request shape, reachable from the
// OpenAI-compatible endpoint by passing the path as a form field instead of a
// file part.
func (p *Provider) transcribeServerPath(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	audioPath := serverAudioPath(req.Fields)
	if audioPath == "" {
		return nil, core.NewInvalidRequestError(
			"file is required, or an audio field naming a path the audio.cpp server can read", nil)
	}

	body := map[string]any{"model": req.Model, "audio": audioPath}
	if language := strings.TrimSpace(req.Language); language != "" {
		body["language"] = language
	}
	// audio.cpp calls the recognition-context field "text"; "prompt" is the
	// OpenAI name for the same thing.
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		body["text"] = prompt
	}
	if err := addNativeTranscriptionFields(body, req.Fields); err != nil {
		return nil, err
	}

	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode audiocpp transcription request", err)
	}
	raw, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v1/audio/transcriptions",
		Model:    req.Model,
		RawBody:  rawBody,
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	if err != nil {
		return nil, err
	}
	return transcriptionResponse(req, raw)
}

// serverAudioPath returns the server-local path the caller named, in the order
// audio.cpp resolves the fields itself.
func serverAudioPath(fields []core.FormField) string {
	for _, name := range audioPathFields {
		for _, field := range fields {
			if field.Name == name && strings.TrimSpace(field.Value) != "" {
				return field.Value
			}
		}
	}
	return ""
}

// addNativeTranscriptionFields copies the forwarded form values the JSON
// request shape understands, restoring the JSON type each one crossed the
// gateway as a string: audio.cpp reads them as a boolean and a number, and a
// string in either place fails the request. The rest of the forwarded values
// are multipart-only and have no place in this body.
func addNativeTranscriptionFields(body map[string]any, fields []core.FormField) error {
	for _, field := range fields {
		value := strings.TrimSpace(field.Value)
		switch field.Name {
		case "stream":
			body["stream"] = value == "true" || value == "True" || value == "1"
		case "busy_timeout_ms":
			milliseconds, err := strconv.Atoi(value)
			if err != nil {
				return core.NewInvalidRequestError("busy_timeout_ms must be an integer", err)
			}
			body["busy_timeout_ms"] = milliseconds
		}
	}
	return nil
}

// transcriptionMultipart streams the OpenAI-style upload audio.cpp accepts:
// the file part plus the fields its multipart parser reads. Unknown parts are
// ignored upstream, so forwarded fields the gateway does not consume itself
// (stream, busy_timeout_ms, ...) travel verbatim (ADR-0011 rule 1).
func transcriptionMultipart(req *core.AudioTranscriptionRequest, content io.Reader) (io.Reader, string) {
	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = "audio"
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer func() { _ = pw.Close() }()

		fields := [][2]string{
			{"model", req.Model},
			{"language", req.Language},
			{"prompt", req.Prompt},
		}
		for _, field := range req.Fields {
			if core.ReservedAudioTranscriptionFormFields[field.Name] {
				continue
			}
			fields = append(fields, [2]string{field.Name, field.Value})
		}
		for _, field := range fields {
			if strings.TrimSpace(field[1]) == "" {
				continue
			}
			if err := writer.WriteField(field[0], field[1]); err != nil {
				_ = pw.CloseWithError(core.NewInvalidRequestError("failed to write "+field[0]+" field", err))
				return
			}
		}

		part, err := writer.CreateFormFile("file", filename)
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

// transcriptionResponse shapes audio.cpp's reply into the response_format the
// caller asked for. Its JSON already carries OpenAI's "text" member (alongside
// a "timing" object OpenAI has no equivalent for, left in place rather than
// stripped), so json and verbose_json are proxied verbatim and text is served
// from the transcript. A streamed reply is relayed with its own content type,
// as the transcription SSE events are already the OpenAI shape.
func transcriptionResponse(req *core.AudioTranscriptionRequest, raw *llmclient.Response) (*core.AudioResponse, error) {
	if raw == nil {
		return nil, core.NewEmptyProviderResponseError("audiocpp")
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw.ContentType)), "text/event-stream") {
		return &core.AudioResponse{ContentType: raw.ContentType, Data: raw.Body}, nil
	}

	format := strings.ToLower(strings.TrimSpace(req.ResponseFormat))
	if format != "text" {
		return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: raw.Body}, nil
	}
	var upstream struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw.Body, &upstream); err != nil {
		return nil, core.NewProviderError("audiocpp", http.StatusBadGateway, "failed to parse transcription response", err)
	}
	return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: []byte(upstream.Text)}, nil
}
