package core

import (
	"io"
	"strings"

	"github.com/goccy/go-json"
)

// AudioSpeechRequest is an OpenAI-compatible POST /v1/audio/speech
// (text-to-speech) request.
type AudioSpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	Instructions   string  `json:"instructions,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string `json:"provider,omitempty"`
}

// AudioTranscriptionRequest is an OpenAI-compatible POST /v1/audio/transcriptions
// (speech-to-text) request. The upstream call is multipart/form-data, so the audio
// bytes and form fields are transport data rather than a JSON body.
type AudioTranscriptionRequest struct {
	Model                  string
	Filename               string
	FileContentType        string
	File                   []byte
	FileReader             io.Reader
	Language               string
	Prompt                 string
	ResponseFormat         string
	Temperature            string
	TimestampGranularities []string

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string
}

// AudioResponse wraps an opaque audio or transcription payload with its content
// type. Speech returns binary audio; transcription returns JSON or text depending
// on response_format. In both cases the gateway proxies the bytes verbatim.
type AudioResponse struct {
	ContentType string
	Data        []byte
}

// DecodeAudioSpeechRequest decodes a JSON text-to-speech request body. The
// semantic envelope is unused: audio responses are binary and not response-cached.
func DecodeAudioSpeechRequest(body []byte, _ *WhiteBoxPrompt) (*AudioSpeechRequest, error) {
	var req AudioSpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, NewInvalidRequestError("invalid audio speech request: "+err.Error(), err)
	}
	return &req, nil
}

// SpeechResponseContentType maps a text-to-speech response_format to its MIME type.
// An unset format defaults to mp3, matching OpenAI's default.
func SpeechResponseContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "mp3":
		return "audio/mpeg"
	case "opus":
		return "audio/ogg"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	default:
		return "application/octet-stream"
	}
}

// TranscriptionResponseContentType maps a transcription response_format to its MIME
// type. json and verbose_json (and an unset format) are JSON; text, srt and vtt are
// plain text.
func TranscriptionResponseContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text", "srt", "vtt":
		return "text/plain; charset=utf-8"
	default:
		return "application/json"
	}
}
