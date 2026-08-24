package server

import (
	"bytes"
	"encoding/json"
)

// sessionUpdateMarker gates transcription-model pinning: only session.update
// frames can select a model, so a cheap byte scan skips the audio appends that
// dominate the relay hot path.
var sessionUpdateMarker = []byte(`"session.update"`)

// pinTranscriptionModel returns a client-frame mapper for transcription
// sessions that rewrites every session.update to carry the gateway-routed
// model. The relay otherwise forwards frames verbatim, which would let a caller
// authorized (and billed) for one transcription model select another inside the
// session payload. Pinning makes model access, rate limits, and usage
// attribution correct by construction — and lets clients omit the model from
// session.update entirely.
//
// Frames that are not session.update, or that do not parse, are forwarded
// unchanged: the upstream is the authority on rejecting malformed events.
func pinTranscriptionModel(model string) func([]byte) []byte {
	return func(frame []byte) []byte {
		if !bytes.Contains(frame, sessionUpdateMarker) {
			return frame
		}
		var event map[string]any
		if err := json.Unmarshal(frame, &event); err != nil || event["type"] != "session.update" {
			return frame
		}
		session, ok := event["session"].(map[string]any)
		if !ok {
			return frame
		}
		// GA shape: session.audio.input.transcription.model. Missing levels are
		// created so a session.update without a model is pinned too.
		childMap(childMap(childMap(session, "audio"), "input"), "transcription")["model"] = model
		// Legacy beta shape: session.input_audio_transcription.model. Only pinned
		// when the client sent it, so the gateway never introduces the old field.
		if legacy, ok := session["input_audio_transcription"].(map[string]any); ok {
			legacy["model"] = model
		}
		pinned, err := json.Marshal(event)
		if err != nil {
			return frame
		}
		return pinned
	}
}

// childMap returns parent[key] as an object, creating it when absent or of
// another type.
func childMap(parent map[string]any, key string) map[string]any {
	if m, ok := parent[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	parent[key] = m
	return m
}
