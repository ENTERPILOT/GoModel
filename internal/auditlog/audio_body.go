package auditlog

import (
	"bytes"
	"strings"

	"github.com/enterpilot/gomodel/internal/mediastore"
)

// AudioBodyLog is the audit representation of an audio request/response body.
// The "__audio__" marker lets the dashboard detect audio payloads and render a
// player (when MediaID names a stored object) or a labeled placeholder. Rows
// written before ADR-0013 carry the audio inline as base64 under "encoding"
// and "data" instead; the dashboard still renders those.
type AudioBodyLog struct {
	Audio       bool           `json:"__audio__" bson:"__audio__"`
	ContentType string         `json:"content_type,omitempty" bson:"content_type,omitempty"`
	Bytes       int64          `json:"bytes" bson:"bytes"`
	MediaID     string         `json:"media_id,omitempty" bson:"media_id,omitempty"`
	Stored      bool           `json:"stored" bson:"stored"`
	Meta        map[string]any `json:"meta,omitempty" bson:"meta,omitempty"`
}

// IsAudioContentType reports whether a Content-Type denotes an audio payload.
func IsAudioContentType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.TrimSpace(mediaType)
	return len(mediaType) >= 6 && strings.EqualFold(mediaType[:6], "audio/")
}

// BuildAudioResponseBody builds the audit value for a binary audio response,
// storing the bytes through capture when it is non-nil.
func BuildAudioResponseBody(capture *MediaCapture, contentType string, data []byte) AudioBodyLog {
	return buildAudioBody(capture, contentType, data, nil)
}

// BuildAudioUploadBody builds the audit value for an uploaded audio request
// (e.g. a transcription input). It behaves like BuildAudioResponseBody but
// attaches request metadata (model, params) alongside the audio so the
// dashboard can show both a player and the parameters.
func BuildAudioUploadBody(capture *MediaCapture, contentType string, data []byte, meta map[string]any) AudioBodyLog {
	return buildAudioBody(capture, contentType, data, meta)
}

// BuildRelayedAudioResponseBody builds the audit value for an audio response
// that was teed into w while it streamed to the client. It commits the
// stored object, so it is called exactly once per relay.
func BuildRelayedAudioResponseBody(w *MediaWriter) AudioBodyLog {
	body := AudioBodyLog{
		Audio:       true,
		ContentType: strings.TrimSpace(w.contentType),
		Bytes:       w.Bytes(),
	}
	body.attach(w.finish())
	return body
}

func buildAudioBody(capture *MediaCapture, contentType string, data []byte, meta map[string]any) AudioBodyLog {
	body := AudioBodyLog{
		Audio:       true,
		ContentType: strings.TrimSpace(contentType),
		Bytes:       int64(len(data)),
		Meta:        meta,
	}
	if len(data) > 0 {
		body.attach(capture.save(mediastore.KindAudio, contentType, bytes.NewReader(data)))
	}
	return body
}

func (b *AudioBodyLog) attach(object *mediastore.Object) {
	if object == nil {
		return
	}
	b.MediaID = object.ID
	b.Stored = true
}
