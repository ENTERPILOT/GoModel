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
	Model string `json:"model"`
	Input string `json:"input"`
	// Voice is a voice name or ID. It may be sent as a string ("alloy") or as
	// an OpenAI custom-voice object ({"id": "voice_1234"}).
	Voice          string  `json:"voice,omitempty"`
	Instructions   string  `json:"instructions,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string `json:"provider,omitempty"`

	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`

	// customVoice is set when the client sent voice as an object. Voice then
	// holds its id for providers with their own wire format, and the object is
	// marshaled back unchanged for OpenAI-compatible upstreams.
	customVoice *speechVoiceObject
}

// audioSpeechRequestFields are the typed members of AudioSpeechRequest, derived
// from the struct so the unknown-field list cannot drift.
var audioSpeechRequestFields = jsonFieldSetOf(AudioSpeechRequest{})

func (r *AudioSpeechRequest) UnmarshalJSON(data []byte) error {
	// alias drops UnmarshalJSON so json.Unmarshal does not recurse; ExtraFields
	// is json:"-" and captured separately. The shallower Voice shadows the
	// alias's string field so the object form decodes too.
	type alias AudioSpeechRequest
	var raw struct {
		alias
		Voice json.RawMessage `json:"voice"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	voice, customVoice, err := decodeSpeechVoice(raw.Voice)
	if err != nil {
		return wrapJSONMemberDecodeError("voice", err)
	}
	extraFields, err := extractUnknownJSONFieldsSet(data, audioSpeechRequestFields)
	if err != nil {
		return err
	}
	*r = AudioSpeechRequest(raw.alias)
	r.Voice = voice
	r.customVoice = customVoice
	r.ExtraFields = extraFields
	return nil
}

// MarshalJSON emits voice in the form the client sent it. The object is sent
// only while Voice still names its id, so code that replaces Voice sends the
// replacement.
func (r AudioSpeechRequest) MarshalJSON() ([]byte, error) {
	type alias AudioSpeechRequest
	if r.customVoice == nil || r.customVoice.id != r.Voice {
		return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
	}
	return marshalWithUnknownJSONFields(struct {
		alias
		Voice json.RawMessage `json:"voice"`
	}{alias: alias(r), Voice: r.customVoice.raw}, r.ExtraFields)
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
	// Stream carries a body the provider is still producing, so the gateway can
	// relay it as it arrives instead of holding the whole generation: chunked
	// speech audio and transcription server-sent events both trade badly for
	// time-to-first-byte when buffered. It is mutually exclusive with Data, and
	// the caller owns draining and closing it.
	Stream io.ReadCloser
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
