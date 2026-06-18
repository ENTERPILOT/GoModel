package xiaomi

import (
	"context"
	"encoding/base64"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
)

// Xiaomi MiMo has no /audio/speech or /audio/transcriptions endpoints: TTS and
// ASR run through /chat/completions with MiMo-specific conventions
// (https://platform.xiaomimimo.com/docs/en-US/usage-guide/speech-synthesis-v2.5).
// CreateSpeech and CreateTranscription translate the standard OpenAI audio
// requests into that dialect so regular audio clients work unchanged.

var _ core.AudioProvider = (*Provider)(nil)

// CreateSpeech synthesizes speech by translating the request into a MiMo TTS
// chat completion: the synthesis text goes in an assistant message, optional
// style instructions in a user message, and voice/format in the top-level
// audio parameter. The base64 audio returned in message.audio.data is decoded
// back into binary.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio speech request is required", nil)
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, core.NewInvalidRequestError("input is required", nil)
	}
	if req.Speed != 0 && req.Speed != 1 {
		return nil, core.NewInvalidRequestError("xiaomi does not support speech speed control; use instructions to adjust pace", nil)
	}
	format, contentType, err := speechFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}

	audioParams := map[string]string{"format": format}
	if voice := strings.TrimSpace(req.Voice); voice != "" {
		audioParams["voice"] = voice
	}
	rawAudio, err := json.Marshal(audioParams)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode audio parameters", err)
	}

	var messages []core.Message
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		messages = append(messages, core.Message{Role: "user", Content: instructions})
	}
	messages = append(messages, core.Message{Role: "assistant", Content: req.Input})

	resp, err := p.ChatCompletion(ctx, &core.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"audio": rawAudio}),
	})
	if err != nil {
		return nil, err
	}

	audio, err := decodeSpeechAudio(resp)
	if err != nil {
		return nil, err
	}
	return &core.AudioResponse{ContentType: contentType, Data: audio}, nil
}

// speechFormat maps an OpenAI response_format to MiMo's audio.format values.
// MiMo only synthesizes wav and pcm16; wav is the default.
func speechFormat(responseFormat string) (format, contentType string, err error) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "wav":
		return "wav", "audio/wav", nil
	case "pcm":
		return "pcm16", "audio/pcm", nil
	default:
		return "", "", core.NewInvalidRequestError("xiaomi supports wav or pcm response formats", nil)
	}
}

func decodeSpeechAudio(resp *core.ChatResponse) ([]byte, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return nil, core.NewProviderError("xiaomi", 502, "speech response contains no choices", nil)
	}
	rawAudio := resp.Choices[0].Message.ExtraFields.Lookup("audio")
	if len(rawAudio) == 0 {
		return nil, core.NewProviderError("xiaomi", 502, "speech response contains no audio", nil)
	}
	var audio struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(rawAudio, &audio); err != nil || audio.Data == "" {
		return nil, core.NewProviderError("xiaomi", 502, "speech response audio is malformed", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(audio.Data)
	if err != nil {
		return nil, core.NewProviderError("xiaomi", 502, "speech response audio is not valid base64", err)
	}
	return decoded, nil
}

// CreateTranscription transcribes audio by translating the request into a MiMo
// ASR chat completion: the audio bytes are sent as a base64 data: URI in an
// input_audio content part, with the language passed via asr_options.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	switch strings.ToLower(strings.TrimSpace(req.ResponseFormat)) {
	case "", "json", "text":
	default:
		return nil, core.NewInvalidRequestError("xiaomi transcription supports json or text response formats", nil)
	}
	if strings.TrimSpace(req.Prompt) != "" {
		return nil, core.NewInvalidRequestError("xiaomi transcription does not support prompt", nil)
	}
	for _, granularity := range req.TimestampGranularities {
		if strings.TrimSpace(granularity) != "" {
			return nil, core.NewInvalidRequestError("xiaomi transcription does not support timestamp_granularities", nil)
		}
	}
	audioBytes, err := transcriptionAudioBytes(req)
	if err != nil {
		return nil, err
	}

	dataURI := "data:" + transcriptionMIMEType(req) + ";base64," + base64.StdEncoding.EncodeToString(audioBytes)
	chatReq := &core.ChatRequest{
		Model: req.Model,
		Messages: []core.Message{{
			Role: "user",
			Content: []core.ContentPart{{
				Type:       "input_audio",
				InputAudio: &core.InputAudioContent{Data: dataURI},
			}},
		}},
	}
	if language := strings.TrimSpace(req.Language); language != "" {
		rawOptions, err := json.Marshal(map[string]string{"language": language})
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to encode asr options", err)
		}
		chatReq.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"asr_options": rawOptions})
	}
	if rawTemperature := strings.TrimSpace(req.Temperature); rawTemperature != "" {
		temperature, err := strconv.ParseFloat(rawTemperature, 64)
		if err != nil {
			return nil, core.NewInvalidRequestError("temperature must be a number", err)
		}
		chatReq.Temperature = &temperature
	}

	resp, err := p.ChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return nil, core.NewProviderError("xiaomi", 502, "transcription response contains no choices", nil)
	}
	text := core.ExtractTextContent(resp.Choices[0].Message.Content)

	if strings.EqualFold(strings.TrimSpace(req.ResponseFormat), "text") {
		return &core.AudioResponse{
			ContentType: core.TranscriptionResponseContentType(req.ResponseFormat),
			Data:        []byte(text),
		}, nil
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, core.NewProviderError("xiaomi", 502, "failed to encode transcription response", err)
	}
	return &core.AudioResponse{
		ContentType: core.TranscriptionResponseContentType(req.ResponseFormat),
		Data:        body,
	}, nil
}

func transcriptionAudioBytes(req *core.AudioTranscriptionRequest) ([]byte, error) {
	if len(req.File) > 0 {
		return req.File, nil
	}
	if req.FileReader != nil {
		audioBytes, err := io.ReadAll(req.FileReader)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to read audio file", err)
		}
		if len(audioBytes) > 0 {
			return audioBytes, nil
		}
	}
	return nil, core.NewInvalidRequestError("file is required", nil)
}

// transcriptionMIMEType resolves the audio MIME type from the upload's content
// type when present, falling back to the filename extension, then to wav.
func transcriptionMIMEType(req *core.AudioTranscriptionRequest) string {
	if contentType := strings.TrimSpace(req.FileContentType); strings.HasPrefix(contentType, "audio/") {
		return contentType
	}
	switch strings.ToLower(path.Ext(strings.TrimSpace(req.Filename))) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	default:
		return "audio/wav"
	}
}
