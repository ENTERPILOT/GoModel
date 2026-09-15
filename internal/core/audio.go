package core

import (
	"io"
	"strings"

	"github.com/goccy/go-json"
)

// AudioSpeechRequest is an OpenAI-compatible POST /v1/audio/speech
// (text-to-speech) request. Only the fields the gateway needs to route,
// validate, and audit are typed; every other member (stream_format,
// model-specific extras, ...) is preserved verbatim in ExtraFields and
// forwarded upstream so new provider parameters work without a gateway change
// (ADR-0011 rule 1).
type AudioSpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	Instructions   string  `json:"instructions,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string `json:"provider,omitempty"`

	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`
}

// audioSpeechRequestFields are the typed members of AudioSpeechRequest, derived
// from the struct so the unknown-field list cannot drift.
var audioSpeechRequestFields = jsonFieldSetOf(AudioSpeechRequest{})

func (r *AudioSpeechRequest) UnmarshalJSON(data []byte) error {
	// alias drops UnmarshalJSON so json.Unmarshal does not recurse; ExtraFields
	// is json:"-" and captured separately.
	type alias AudioSpeechRequest
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFieldsSet(data, audioSpeechRequestFields)
	if err != nil {
		return err
	}
	*r = AudioSpeechRequest(raw)
	r.ExtraFields = extraFields
	return nil
}

func (r AudioSpeechRequest) MarshalJSON() ([]byte, error) {
	type alias AudioSpeechRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
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
	// Fields carries the form values the gateway does not consume itself
	// (include[], chunking_strategy, stream, provider-native extras, ...). They
	// are forwarded upstream verbatim (ADR-0011 rule 1). Repeated names keep
	// their relative order; ordering across different names is not preserved
	// (multipart forms have no cross-field order semantics).
	Fields []FormField

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string
}

// ReservedAudioTranscriptionFormFields are the multipart fields the gateway
// consumes and re-emits itself. They never travel in Fields, so a client
// cannot overwrite a gateway-controlled part (model, file, routing metadata)
// through the passthrough path.
var ReservedAudioTranscriptionFormFields = map[string]bool{
	"model":                     true,
	"file":                      true,
	"provider":                  true,
	"language":                  true,
	"prompt":                    true,
	"response_format":           true,
	"temperature":               true,
	"timestamp_granularities":   true,
	"timestamp_granularities[]": true,
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
