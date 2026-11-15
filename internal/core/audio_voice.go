package core

import (
	"bytes"
	"errors"
	"strings"

	"github.com/goccy/go-json"
)

// speechVoiceObject is a voice the client sent in OpenAI's custom-voice object
// form ({"id": "voice_1234"}): its id and the object exactly as received, so
// members the gateway does not know still reach OpenAI-compatible upstreams.
type speechVoiceObject struct {
	id  string
	raw json.RawMessage
}

var errInvalidSpeechVoice = errors.New(`must be a string or an object with a non-empty string "id", for example {"id": "voice_1234"}`)

// decodeSpeechVoice accepts voice as a string or as an object with a string
// id. Absent and null mean no voice.
func decodeSpeechVoice(raw json.RawMessage) (string, *speechVoiceObject, error) {
	trimmed := bytes.TrimSpace(raw)
	if IsJSONNull(trimmed) {
		return "", nil, nil
	}
	switch trimmed[0] {
	case '"':
		var voice string
		if err := json.Unmarshal(trimmed, &voice); err != nil {
			return "", nil, errInvalidSpeechVoice
		}
		return voice, nil, nil
	case '{':
		var object struct {
			ID *string `json:"id"`
		}
		if err := json.Unmarshal(trimmed, &object); err != nil || object.ID == nil || strings.TrimSpace(*object.ID) == "" {
			return "", nil, errInvalidSpeechVoice
		}
		return *object.ID, &speechVoiceObject{id: *object.ID, raw: CloneRawJSON(trimmed)}, nil
	default:
		return "", nil, errInvalidSpeechVoice
	}
}

// WithStringVoice returns r, or a copy that sends a voice object's id as a
// plain string, for upstreams that take OpenAI's body shape but only a string
// voice.
func (r *AudioSpeechRequest) WithStringVoice() *AudioSpeechRequest {
	if r == nil || r.customVoice == nil {
		return r
	}
	plain := *r
	plain.customVoice = nil
	return &plain
}
