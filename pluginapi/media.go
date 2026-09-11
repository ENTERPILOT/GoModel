package pluginapi

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// DecodeMedia returns the inline payload of an image or audio part and its
// media type: an image carried as a data: URI, or audio carried as base64.
// ok is false for a part referenced by URL (nothing inline to decode), for
// other part kinds, and for a payload that does not decode. It is the
// reader half of [Prompt.SetMedia].
func (p Part) DecodeMedia() (data []byte, mediaType string, ok bool) {
	switch p.Kind {
	case PartImage:
		return decodeDataURI(p.URL)
	case PartAudio:
		payload := string(p.Data)
		if strings.HasPrefix(payload, "data:") {
			return decodeDataURI(payload)
		}
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || len(decoded) == 0 {
			return nil, "", false
		}
		return decoded, p.MediaType, true
	default:
		return nil, "", false
	}
}

// SetMedia replaces the payload of the image or audio part partIdx of
// message msgID with data of the given media type ("image/png",
// "audio/wav"). The part is re-encoded in the wire format the message
// arrived in: a data: URI for an image, base64 input audio for chat audio;
// other members of the wire part, such as an image's detail hint, are kept.
// Use [Prompt.SetToolResult] for media inside a tool result.
func (p *Prompt) SetMedia(msgID string, partIdx int, mediaType string, data []byte) error {
	m := p.Message(msgID)
	if m == nil {
		return unknownMessage(msgID)
	}
	if partIdx < 0 || partIdx >= len(m.Parts) {
		return fmt.Errorf("pluginapi: message %q has no part %d", msgID, partIdx)
	}
	part := &m.Parts[partIdx]
	if part.Kind != PartImage && part.Kind != PartAudio {
		return fmt.Errorf("pluginapi: part %d of message %q is %s, not image or audio", partIdx, msgID, part.Kind)
	}
	mediaType = strings.TrimSpace(mediaType)
	if !strings.Contains(mediaType, "/") {
		return fmt.Errorf("pluginapi: media type %q is not a MIME type", mediaType)
	}
	if prefix := string(part.Kind) + "/"; !strings.HasPrefix(mediaType, prefix) {
		return fmt.Errorf("pluginapi: media type %q does not fit the %s part %d of message %q", mediaType, part.Kind, partIdx, msgID)
	}
	if len(data) == 0 {
		return fmt.Errorf("pluginapi: media data for part %d of message %q is empty", partIdx, msgID)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	switch part.Kind {
	case PartImage:
		part.URL = "data:" + mediaType + ";base64," + encoded
		part.Data = nil
	case PartAudio:
		part.Data = []byte(encoded)
		part.URL = ""
	}
	part.MediaType = mediaType
	part.Raw = nil
	p.changes.mark(msgID, ChangeEdited)
	return nil
}

func decodeDataURI(s string) ([]byte, string, bool) {
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return nil, "", false
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, "", false
	}
	mediaType, params, _ := strings.Cut(meta, ";")
	if !strings.Contains(mediaType, "/") || !strings.Contains(";"+params+";", ";base64;") {
		return nil, "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(decoded) == 0 {
		return nil, "", false
	}
	return decoded, mediaType, true
}
