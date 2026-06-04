package auditlog

import (
	"encoding/base64"
	"strings"
)

// audioBodyMaxBytes caps how much *raw* audio is embedded as base64 in an audit
// log entry; larger payloads are recorded as metadata-only placeholders so the
// audit store does not balloon on long generations. The stored base64 is ~4/3 of
// this (≈10.7 MB at the cap), deliberately kept well under document-store
// per-record ceilings (e.g. MongoDB's 16 MB BSON limit) so a near-cap clip plus
// the rest of the entry (request text, headers, workflow metadata) and encoding
// overhead cannot push the document over the limit and fail the audit insert.
const audioBodyMaxBytes = 8 * 1024 * 1024

// AudioBodyLog is the audit representation of an audio request/response body.
// The "__audio__" marker lets the dashboard detect audio payloads and render a
// player (when Data is present) or a labeled placeholder. When Data is set it
// holds the base64-encoded audio, suitable for a data: URL of ContentType.
type AudioBodyLog struct {
	Audio       bool           `json:"__audio__" bson:"__audio__"`
	ContentType string         `json:"content_type,omitempty" bson:"content_type,omitempty"`
	Bytes       int            `json:"bytes" bson:"bytes"`
	Encoding    string         `json:"encoding,omitempty" bson:"encoding,omitempty"`
	Data        string         `json:"data,omitempty" bson:"data,omitempty"`
	Stored      bool           `json:"stored" bson:"stored"`
	TooLarge    bool           `json:"too_large,omitempty" bson:"too_large,omitempty"`
	Meta        map[string]any `json:"meta,omitempty" bson:"meta,omitempty"`
}

// IsAudioContentType reports whether a Content-Type denotes an audio payload.
func IsAudioContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return strings.HasPrefix(mediaType, "audio/")
}

// BuildAudioResponseBody builds the audit value for a binary audio response.
// When storeBytes is true and the payload fits within audioBodyMaxBytes the
// audio is embedded as base64 for playback; otherwise only metadata is kept.
func BuildAudioResponseBody(contentType string, data []byte, storeBytes bool) AudioBodyLog {
	return buildAudioBody(contentType, data, storeBytes, nil)
}

// BuildAudioUploadBody builds the audit value for an uploaded audio request
// (e.g. a transcription input). It behaves like BuildAudioResponseBody but
// attaches request metadata (model, params) alongside the audio so the
// dashboard can show both a player and the parameters.
func BuildAudioUploadBody(contentType string, data []byte, storeBytes bool, meta map[string]any) AudioBodyLog {
	return buildAudioBody(contentType, data, storeBytes, meta)
}

func buildAudioBody(contentType string, data []byte, storeBytes bool, meta map[string]any) AudioBodyLog {
	body := AudioBodyLog{
		Audio:       true,
		ContentType: strings.TrimSpace(contentType),
		Bytes:       len(data),
		Meta:        meta,
	}
	if !storeBytes || len(data) == 0 {
		return body
	}
	if len(data) > audioBodyMaxBytes {
		body.TooLarge = true
		return body
	}
	body.Encoding = "base64"
	body.Data = base64.StdEncoding.EncodeToString(data)
	body.Stored = true
	return body
}
