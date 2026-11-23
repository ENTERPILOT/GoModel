package elevenlabs

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/goccy/go-json"
	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

// OpenAI realtime events the translator reads from the client and writes back.
const (
	eventAudioAppend         = "input_audio_buffer.append"
	eventAudioCommit         = "input_audio_buffer.commit"
	eventAudioCommitted      = "input_audio_buffer.committed"
	eventSessionUpdate       = "session.update"
	eventLegacySessionUpdate = "transcription_session.update"
	eventTranscriptDelta     = "conversation.item.input_audio_transcription.delta"
	eventTranscriptCompleted = "conversation.item.input_audio_transcription.completed"
)

// realtimeErrorTypes maps the Scribe Realtime error messages that are not a
// plain provider failure to the error type the client sees. Anything missing
// here, including message types added later, is a provider error.
var realtimeErrorTypes = map[string]core.ErrorType{
	"auth_error":                  core.ErrorTypeAuthentication,
	"quota_exceeded":              core.ErrorTypeRateLimit,
	"rate_limited":                core.ErrorTypeRateLimit,
	"commit_throttled":            core.ErrorTypeRateLimit,
	"queue_overflow":              core.ErrorTypeRateLimit,
	"resource_exhausted":          core.ErrorTypeRateLimit,
	"input_error":                 core.ErrorTypeInvalidRequest,
	"invalid_request":             core.ErrorTypeInvalidRequest,
	"chunk_size_exceeded":         core.ErrorTypeInvalidRequest,
	"insufficient_audio_activity": core.ErrorTypeInvalidRequest,
}

// realtimeTranslator converts one Scribe Realtime session to and from the
// OpenAI realtime transcription events. The client appends and commits audio
// and receives transcript deltas and completed transcripts; ElevenLabs takes
// input_audio_chunk messages and answers with partial and committed
// transcripts.
//
// The session's audio format and commit strategy are fixed when the gateway
// dials, so a session.update is answered here with the configuration in effect
// instead of being forwarded.
type realtimeTranslator struct {
	model string

	// mu guards the fields below: client events and provider messages arrive on
	// different goroutines.
	mu         sync.Mutex
	sessionID  string
	config     realtimeSessionConfig
	itemID     string // segment being transcribed; empty between segments
	lastItemID string // segment committed before it
	sent       string // transcript text the client already holds for itemID
}

func newRealtimeTranslator(model string) *realtimeTranslator {
	return &realtimeTranslator{model: model}
}

// realtimeSessionConfig is the part of the provider's session_started config
// the client is told about.
type realtimeSessionConfig struct {
	LanguageCode            string  `json:"language_code"`
	VADThreshold            float64 `json:"vad_threshold"`
	VADSilenceThresholdSecs float64 `json:"vad_silence_threshold_secs"`
}

// inputAudioChunk is the only message Scribe Realtime accepts from a client.
type inputAudioChunk struct {
	MessageType string `json:"message_type"`
	AudioBase64 string `json:"audio_base_64"`
	Commit      bool   `json:"commit"`
	SampleRate  int    `json:"sample_rate"`
}

// ClientEvent implements core.RealtimeTranslator.
func (t *realtimeTranslator) ClientEvent(frame []byte) (upstream, replies [][]byte) {
	var event struct {
		Type    string          `json:"type"`
		EventID string          `json:"event_id"`
		Audio   string          `json:"audio"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(frame, &event); err != nil {
		return nil, frames(errorEvent(core.ErrorTypeInvalidRequest, "invalid_event", "event is not valid JSON", ""))
	}
	switch event.Type {
	case eventAudioAppend:
		if event.Audio == "" {
			return nil, frames(errorEvent(core.ErrorTypeInvalidRequest, "missing_audio", "audio is required", event.EventID))
		}
		return frames(audioChunk(event.Audio, false)), nil
	case eventAudioCommit:
		// Scribe Realtime commits with a flag on an audio chunk; an empty chunk
		// commits what was appended so far.
		return frames(audioChunk("", true)), nil
	case eventSessionUpdate, eventLegacySessionUpdate:
		return nil, frames(t.sessionUpdated(event.Type, event.Session, event.EventID))
	default:
		// The message names what is supported rather than echoing the type back.
		return nil, frames(errorEvent(core.ErrorTypeInvalidRequest, "unsupported_event",
			"elevenlabs realtime transcription sessions support only the input_audio_buffer.append, input_audio_buffer.commit, and session.update events",
			event.EventID))
	}
}

func audioChunk(audio string, commit bool) []byte {
	return marshal(inputAudioChunk{
		MessageType: "input_audio_chunk",
		AudioBase64: audio,
		Commit:      commit,
		SampleRate:  realtimeSampleRate,
	})
}

// sessionUpdated answers a session.update. The audio format cannot change after
// the dial, and audio in another format would be decoded as noise, so asking for
// one is an error. Every other setting is answered with the configuration in
// effect, which is how the client learns what was not applied.
func (t *realtimeTranslator) sessionUpdated(eventType string, session json.RawMessage, eventID string) []byte {
	var update struct {
		InputAudioFormat string `json:"input_audio_format"` // legacy beta field
		Audio            struct {
			Input struct {
				Format *struct {
					Type string `json:"type"`
					Rate int    `json:"rate"`
				} `json:"format"`
			} `json:"input"`
		} `json:"audio"`
	}
	if len(session) > 0 {
		if err := json.Unmarshal(session, &update); err != nil {
			return errorEvent(core.ErrorTypeInvalidRequest, "invalid_session", "session is not a valid session object", eventID)
		}
	}
	format := update.Audio.Input.Format
	unsupported := update.InputAudioFormat != "" && update.InputAudioFormat != "pcm16"
	if format != nil {
		unsupported = unsupported ||
			(format.Type != "" && format.Type != "audio/pcm") ||
			(format.Rate != 0 && format.Rate != realtimeSampleRate)
	}
	if unsupported {
		return errorEvent(core.ErrorTypeInvalidRequest, "unsupported_audio_format",
			"elevenlabs realtime transcription sessions accept PCM16 audio at 24 kHz only", eventID)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	return marshal(map[string]any{
		"type":     strings.TrimSuffix(eventType, "update") + "updated",
		"event_id": newRealtimeID("event"),
		"session":  t.sessionLocked(),
	})
}

// sessionLocked describes the session in the OpenAI transcription session
// shape. The caller holds mu.
func (t *realtimeTranslator) sessionLocked() map[string]any {
	transcription := map[string]any{"model": t.model}
	if t.config.LanguageCode != "" {
		transcription["language"] = t.config.LanguageCode
	}
	turnDetection := map[string]any{"type": "server_vad"}
	if t.config.VADThreshold > 0 {
		turnDetection["threshold"] = t.config.VADThreshold
	}
	if t.config.VADSilenceThresholdSecs > 0 {
		turnDetection["silence_duration_ms"] = int(t.config.VADSilenceThresholdSecs * 1000)
	}
	session := map[string]any{
		"type":   "transcription",
		"object": "realtime.transcription_session",
		"audio": map[string]any{"input": map[string]any{
			"format":         map[string]any{"type": "audio/pcm", "rate": realtimeSampleRate},
			"transcription":  transcription,
			"turn_detection": turnDetection,
		}},
	}
	if t.sessionID != "" {
		session["id"] = "sess_" + t.sessionID
	}
	return session
}

// ProviderEvent implements core.RealtimeTranslator.
func (t *realtimeTranslator) ProviderEvent(frame []byte) [][]byte {
	var message struct {
		MessageType string                `json:"message_type"`
		Text        string                `json:"text"`
		Error       json.RawMessage       `json:"error"`
		SessionID   string                `json:"session_id"`
		Config      realtimeSessionConfig `json:"config"`
	}
	if err := json.Unmarshal(frame, &message); err != nil {
		return nil
	}
	switch message.MessageType {
	case "session_started":
		t.mu.Lock()
		defer t.mu.Unlock()
		t.sessionID, t.config = message.SessionID, message.Config
		return frames(marshal(map[string]any{
			"type":     "session.created",
			"event_id": newRealtimeID("event"),
			"session":  t.sessionLocked(),
		}))
	case "partial_transcript":
		return t.partial(message.Text)
	case "committed_transcript":
		return t.committed(message.Text)
	}
	if len(message.Error) == 0 {
		// Timestamped duplicates of a committed transcript, warnings, and
		// messages for features the gateway does not enable have no event in the
		// OpenAI schema.
		return nil
	}
	errorType, known := realtimeErrorTypes[message.MessageType]
	if !known {
		errorType = core.ErrorTypeProvider
	}
	if errorType != core.ErrorTypeInvalidRequest {
		// The error text stays out of the log; the type is enough to tell a bad
		// provider key from a quota or an outage.
		slog.Warn("elevenlabs realtime session error", "error_type", message.MessageType)
	}
	return frames(errorEvent(errorType, message.MessageType, errorText(message.Error), ""))
}

// partial turns a partial transcript into a transcript delta. ElevenLabs sends
// the whole segment so far each time, and may revise words it sent before;
// OpenAI deltas only ever append. So the delta is what the new text adds to the
// text the client already holds. A partial that does not extend that text sends
// nothing, so after a revision the segment sends no more deltas: the completed
// event carries the corrected transcript.
func (t *realtimeTranslator) partial(text string) [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	delta, extends := strings.CutPrefix(text, t.sent)
	if !extends || delta == "" {
		return nil
	}
	t.sent = text
	return frames(marshal(map[string]any{
		"type":          eventTranscriptDelta,
		"event_id":      newRealtimeID("event"),
		"item_id":       t.currentItemLocked(),
		"content_index": 0,
		"delta":         delta,
	}))
}

// committed closes the current segment: the audio buffer is committed and the
// segment's final transcript is complete.
func (t *realtimeTranslator) committed(text string) [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	itemID := t.currentItemLocked()
	var previous any
	if t.lastItemID != "" {
		previous = t.lastItemID
	}
	t.itemID, t.lastItemID, t.sent = "", itemID, ""
	return frames(
		marshal(map[string]any{
			"type":             eventAudioCommitted,
			"event_id":         newRealtimeID("event"),
			"previous_item_id": previous,
			"item_id":          itemID,
		}),
		marshal(map[string]any{
			"type":          eventTranscriptCompleted,
			"event_id":      newRealtimeID("event"),
			"item_id":       itemID,
			"content_index": 0,
			"transcript":    text,
		}),
	)
}

// currentItemLocked returns the id of the segment being transcribed, starting
// one when the previous segment was committed. The caller holds mu.
func (t *realtimeTranslator) currentItemLocked() string {
	if t.itemID == "" {
		t.itemID = newRealtimeID("item")
	}
	return t.itemID
}

// errorEvent builds an OpenAI realtime error event. clientEventID names the
// client event that caused it, when there is one.
func errorEvent(errorType core.ErrorType, code, message, clientEventID string) []byte {
	var causedBy any
	if clientEventID != "" {
		causedBy = clientEventID
	}
	return marshal(map[string]any{
		"type":     "error",
		"event_id": newRealtimeID("event"),
		"error": map[string]any{
			"type":     string(errorType),
			"code":     code,
			"message":  message,
			"param":    nil,
			"event_id": causedBy,
		},
	})
}

// errorText reads the provider's error field, which is documented as a string;
// any other shape is passed on as its JSON text rather than dropped.
func errorText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func newRealtimeID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// marshal encodes a value that cannot fail to encode: only strings, numbers,
// booleans, and maps of them reach it.
func marshal(v any) []byte {
	encoded, _ := json.Marshal(v)
	return encoded
}

func frames(f ...[]byte) [][]byte {
	return f
}

var _ core.RealtimeTranslator = (*realtimeTranslator)(nil)
