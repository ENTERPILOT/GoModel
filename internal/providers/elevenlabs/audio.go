package elevenlabs

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// speechFormat maps an OpenAI-compatible response_format to the ElevenLabs
// output_format query value. ElevenLabs has no aac/flac encoders, so those
// formats are rejected rather than silently substituted.
func speechFormat(responseFormat string) (openAIFormat, outputFormat string, err error) {
	format := strings.ToLower(strings.TrimSpace(responseFormat))
	if format == "" {
		format = "mp3"
	}
	switch format {
	case "mp3":
		return format, "mp3_44100_128", nil
	case "opus":
		return format, "opus_48000_128", nil
	case "pcm":
		return format, "pcm_44100", nil
	case "wav":
		return format, "wav_44100", nil
	default:
		return "", "", core.NewInvalidRequestError("elevenlabs speech supports mp3, opus, pcm, or wav response formats", nil)
	}
}

// speechSpeed validates the OpenAI speed parameter against ElevenLabs' voice
// setting range. A zero value means "unset" and is left out of the request.
func speechSpeed(speed float64) (*float64, error) {
	if speed == 0 {
		return nil, nil
	}
	if speed < 0.7 || speed > 1.2 {
		return nil, core.NewInvalidRequestError("elevenlabs speech speed must be between 0.7 and 1.2", nil)
	}
	return &speed, nil
}

type speechRequest struct {
	Text         string              `json:"text"`
	ModelID      string              `json:"model_id"`
	VoiceSetting *speechVoiceSetting `json:"voice_settings,omitempty"`
}

type speechVoiceSetting struct {
	Speed float64 `json:"speed"`
}

// CreateSpeech translates the OpenAI-compatible request into ElevenLabs'
// POST /v1/text-to-speech/{voice_id} endpoint. The OpenAI "voice" field
// carries the ElevenLabs voice_id directly, since ElevenLabs has no fixed set
// of named voices.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio speech request is required", nil)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, core.NewInvalidRequestError("input is required", nil)
	}
	voiceID := strings.TrimSpace(req.Voice)
	if voiceID == "" {
		return nil, core.NewInvalidRequestError("voice is required and must be an ElevenLabs voice_id", nil)
	}
	if strings.TrimSpace(req.Instructions) != "" {
		return nil, core.NewInvalidRequestError("elevenlabs speech does not support instructions", nil)
	}

	openAIFormat, outputFormat, err := speechFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	speed, err := speechSpeed(req.Speed)
	if err != nil {
		return nil, err
	}

	body := speechRequest{Text: req.Input, ModelID: model}
	if speed != nil {
		body.VoiceSetting = &speechVoiceSetting{Speed: *speed}
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode elevenlabs speech request", err)
	}

	resp, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v1/text-to-speech/" + url.PathEscape(voiceID) + "?output_format=" + outputFormat,
		RawBody:  rawBody,
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, core.ParseProviderError("elevenlabs", resp.StatusCode, resp.Body, nil)
	}
	if len(resp.Body) == 0 {
		return nil, core.NewEmptyProviderResponseError("elevenlabs")
	}

	return &core.AudioResponse{
		ContentType: core.SpeechResponseContentType(openAIFormat),
		Data:        resp.Body,
	}, nil
}

// timestampsGranularity maps the OpenAI timestamp_granularities/response_format
// fields to ElevenLabs' timestamps_granularity value.
func timestampsGranularity(req *core.AudioTranscriptionRequest) string {
	for _, granularity := range req.TimestampGranularities {
		if strings.EqualFold(strings.TrimSpace(granularity), "word") {
			return "word"
		}
	}
	if strings.EqualFold(strings.TrimSpace(req.ResponseFormat), "verbose_json") {
		return "word"
	}
	return "none"
}

type transcriptionResponse struct {
	LanguageCode        string   `json:"language_code"`
	LanguageProbability float64  `json:"language_probability"`
	Text                string   `json:"text"`
	AudioDurationSecs   *float64 `json:"audio_duration_secs"`
	Words               []struct {
		Text  string  `json:"text"`
		Type  string  `json:"type"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	} `json:"words"`
}

// CreateTranscription translates the OpenAI-compatible multipart request to
// ElevenLabs' POST /v1/speech-to-text endpoint.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	switch strings.ToLower(strings.TrimSpace(req.ResponseFormat)) {
	case "", "json", "text", "verbose_json":
	default:
		return nil, core.NewInvalidRequestError("elevenlabs transcription supports json, text, or verbose_json response formats", nil)
	}
	if strings.TrimSpace(req.Prompt) != "" {
		return nil, core.NewInvalidRequestError("elevenlabs transcription does not support prompt", nil)
	}

	content := req.FileReader
	if content == nil && len(req.File) > 0 {
		content = bytes.NewReader(req.File)
	}
	if content == nil {
		return nil, core.NewInvalidRequestError("file is required", nil)
	}

	body, contentType := transcriptionMultipart(req, model, content)
	resp, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:        http.MethodPost,
		Endpoint:      "/v1/speech-to-text",
		RawBodyReader: body,
		Headers:       http.Header{"Content-Type": {contentType}},
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, core.ParseProviderError("elevenlabs", resp.StatusCode, resp.Body, nil)
	}

	var upstream transcriptionResponse
	if err := json.Unmarshal(resp.Body, &upstream); err != nil {
		return nil, core.NewProviderError("elevenlabs", http.StatusBadGateway, "failed to parse transcription response", err)
	}

	return transcriptionAudioResponse(req, &upstream)
}

func transcriptionMultipart(req *core.AudioTranscriptionRequest, model string, content io.Reader) (io.Reader, string) {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer func() { _ = pw.Close() }()

		fields := [...][2]string{
			{"model_id", model},
			{"language_code", strings.TrimSpace(req.Language)},
			{"timestamps_granularity", timestampsGranularity(req)},
		}
		for _, field := range fields {
			if field[1] == "" {
				continue
			}
			if err := writer.WriteField(field[0], field[1]); err != nil {
				_ = pw.CloseWithError(core.NewInvalidRequestError("failed to write "+field[0]+" field", err))
				return
			}
		}

		filename := strings.TrimSpace(req.Filename)
		if filename == "" {
			filename = "audio"
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

// transcriptionAudioResponse shapes ElevenLabs' transcription result into the
// OpenAI response_format the caller asked for.
func transcriptionAudioResponse(req *core.AudioTranscriptionRequest, upstream *transcriptionResponse) (*core.AudioResponse, error) {
	format := strings.ToLower(strings.TrimSpace(req.ResponseFormat))
	if format == "text" {
		return &core.AudioResponse{
			ContentType: core.TranscriptionResponseContentType(format),
			Data:        []byte(upstream.Text),
		}, nil
	}

	if format != "verbose_json" {
		body, err := json.Marshal(map[string]string{"text": upstream.Text})
		if err != nil {
			return nil, core.NewProviderError("elevenlabs", http.StatusBadGateway, "failed to encode transcription response", err)
		}
		return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: body}, nil
	}

	type word struct {
		Word  string  `json:"word"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	}
	var duration float64
	if upstream.AudioDurationSecs != nil {
		duration = *upstream.AudioDurationSecs
	}
	words := make([]word, 0, len(upstream.Words))
	for _, w := range upstream.Words {
		if w.Type != "" && w.Type != "word" {
			continue
		}
		words = append(words, word{Word: w.Text, Start: w.Start, End: w.End})
		if upstream.AudioDurationSecs == nil && w.End > duration {
			duration = w.End
		}
	}
	verbose := struct {
		Task     string  `json:"task"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Text     string  `json:"text"`
		Words    []word  `json:"words,omitempty"`
	}{
		Task:     "transcribe",
		Language: upstream.LanguageCode,
		Duration: duration,
		Text:     upstream.Text,
		Words:    words,
	}
	body, err := json.Marshal(verbose)
	if err != nil {
		return nil, core.NewProviderError("elevenlabs", http.StatusBadGateway, "failed to encode transcription response", err)
	}
	return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: body}, nil
}
