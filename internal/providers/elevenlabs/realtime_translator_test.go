package elevenlabs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeEvent parses one translated frame.
func decodeEvent(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal(frame, &event), "frame = %s", frame)
	return event
}

// decodeEvents parses every translated frame.
func decodeEvents(t *testing.T, frames [][]byte) []map[string]any {
	t.Helper()
	events := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		events = append(events, decodeEvent(t, frame))
	}
	return events
}

func TestRealtimeTranslatorForwardsAudio(t *testing.T) {
	tr := newRealtimeTranslator("scribe_v2_realtime")

	tests := []struct {
		name  string
		event string
		want  string
	}{
		{
			name:  "append becomes an uncommitted chunk",
			event: `{"type":"input_audio_buffer.append","audio":"AAAA"}`,
			want:  `{"message_type":"input_audio_chunk","audio_base_64":"AAAA","commit":false,"sample_rate":24000}`,
		},
		{
			name:  "commit becomes an empty committing chunk",
			event: `{"type":"input_audio_buffer.commit"}`,
			want:  `{"message_type":"input_audio_chunk","audio_base_64":"","commit":true,"sample_rate":24000}`,
		},
		{
			// The event type is decoded, not matched as bytes, so an escaped
			// spelling is still an append.
			name:  "escaped event type is still an append",
			event: `{"type":"input_audio_buffer.app\u0065nd","audio":"AAAA"}`,
			want:  `{"message_type":"input_audio_chunk","audio_base_64":"AAAA","commit":false,"sample_rate":24000}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, replies := tr.ClientEvent([]byte(tt.event))
			assert.Empty(t, replies)
			require.Len(t, upstream, 1)
			assert.JSONEq(t, tt.want, string(upstream[0]))
		})
	}
}

func TestRealtimeTranslatorAnswersUnsupportedEvents(t *testing.T) {
	// Events ElevenLabs has no counterpart for are answered with an error event
	// and never forwarded: the provider would reject them as protocol errors.
	tests := []struct {
		name        string
		event       string
		wantCode    string
		wantEventID any
	}{
		{name: "invalid JSON", event: `{"type":`, wantCode: "invalid_event"},
		{name: "append without audio", event: `{"type":"input_audio_buffer.append","event_id":"evt_1"}`, wantCode: "missing_audio", wantEventID: "evt_1"},
		{name: "buffer clear", event: `{"type":"input_audio_buffer.clear","event_id":"evt_2"}`, wantCode: "unsupported_event", wantEventID: "evt_2"},
		{name: "response.create", event: `{"type":"response.create"}`, wantCode: "unsupported_event"},
		{name: "session is not an object", event: `{"type":"session.update","session":"nope"}`, wantCode: "invalid_session"},
		{
			name:     "G.711 input",
			event:    `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcmu"}}}}}`,
			wantCode: "unsupported_audio_format",
		},
		{
			name:     "another PCM rate",
			event:    `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":16000}}}}}`,
			wantCode: "unsupported_audio_format",
		},
		{
			name:     "legacy G.711 input",
			event:    `{"type":"transcription_session.update","session":{"input_audio_format":"g711_ulaw"}}`,
			wantCode: "unsupported_audio_format",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, replies := newRealtimeTranslator("scribe_v2_realtime").ClientEvent([]byte(tt.event))
			assert.Empty(t, upstream)
			require.Len(t, replies, 1)

			event := decodeEvent(t, replies[0])
			assert.Equal(t, "error", event["type"])
			assert.NotEmpty(t, event["event_id"])
			detail, ok := event["error"].(map[string]any)
			require.True(t, ok, "event = %v", event)
			assert.Equal(t, "invalid_request_error", detail["type"])
			assert.Equal(t, tt.wantCode, detail["code"])
			assert.NotEmpty(t, detail["message"])
			assert.Equal(t, tt.wantEventID, detail["event_id"])
		})
	}
}

func TestRealtimeTranslatorAnswersSessionUpdate(t *testing.T) {
	// The session is configured when the gateway dials, so a session.update is
	// acknowledged with the configuration in effect rather than forwarded. A
	// setting the session cannot take, such as the language here, is absent from
	// the answer.
	tests := []struct {
		name     string
		event    string
		wantType string
	}{
		{
			name:     "GA session.update",
			event:    `{"type":"session.update","session":{"type":"transcription","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"transcription":{"model":"other","language":"pl"},"turn_detection":null}}}}`,
			wantType: "session.updated",
		},
		{
			name:     "legacy transcription_session.update",
			event:    `{"type":"transcription_session.update","session":{"input_audio_format":"pcm16"}}`,
			wantType: "transcription_session.updated",
		},
		{name: "update without a session", event: `{"type":"session.update"}`, wantType: "session.updated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newRealtimeTranslator("scribe_v2_realtime")
			created := tr.ProviderEvent([]byte(`{"message_type":"session_started","session_id":"abc","config":{"language_code":null,"vad_threshold":0.4,"vad_silence_threshold_secs":1.5}}`))
			require.Len(t, created, 1)

			upstream, replies := tr.ClientEvent([]byte(tt.event))
			assert.Empty(t, upstream)
			require.Len(t, replies, 1)

			event := decodeEvent(t, replies[0])
			assert.Equal(t, tt.wantType, event["type"])
			session, err := json.Marshal(event["session"])
			require.NoError(t, err)
			assert.JSONEq(t, `{
				"type": "transcription",
				"object": "realtime.transcription_session",
				"id": "sess_abc",
				"audio": {"input": {
					"format": {"type": "audio/pcm", "rate": 24000},
					"transcription": {"model": "scribe_v2_realtime"},
					"turn_detection": {"type": "server_vad", "threshold": 0.4, "silence_duration_ms": 1500}
				}}
			}`, string(session))
		})
	}
}

func TestRealtimeTranslatorCreatesSession(t *testing.T) {
	tr := newRealtimeTranslator("scribe_v3_live")
	frames := tr.ProviderEvent([]byte(`{"message_type":"session_started","session_id":"abc","config":{"language_code":"en","vad_threshold":0.5,"vad_silence_threshold_secs":0.3}}`))
	require.Len(t, frames, 1)

	event := decodeEvent(t, frames[0])
	assert.Equal(t, "session.created", event["type"])
	assert.NotEmpty(t, event["event_id"])
	session, err := json.Marshal(event["session"])
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type": "transcription",
		"object": "realtime.transcription_session",
		"id": "sess_abc",
		"audio": {"input": {
			"format": {"type": "audio/pcm", "rate": 24000},
			"transcription": {"model": "scribe_v3_live", "language": "en"},
			"turn_detection": {"type": "server_vad", "threshold": 0.5, "silence_duration_ms": 300}
		}}
	}`, string(session))
}

func TestRealtimeTranslatorMapsTranscripts(t *testing.T) {
	// ElevenLabs repeats the whole segment in every partial transcript; the
	// client gets append-only deltas. A partial that rewrites earlier words has
	// no delta, and neither has one that adds nothing; the completed event then
	// carries the final text. A commit closes the segment and the next partial
	// starts a new item linked to the previous one.
	tr := newRealtimeTranslator("scribe_v2_realtime")

	type step struct {
		message string
		want    []map[string]any // type plus the fields that matter; nil means no event
	}
	steps := []step{
		{message: `{"message_type":"partial_transcript","text":"Hello"}`, want: []map[string]any{{"type": eventTranscriptDelta, "delta": "Hello"}}},
		{message: `{"message_type":"partial_transcript","text":"Hello there"}`, want: []map[string]any{{"type": eventTranscriptDelta, "delta": " there"}}},
		{message: `{"message_type":"partial_transcript","text":"Hello there"}`},
		{message: `{"message_type":"partial_transcript","text":"Hello, there"}`},
		{message: `{"message_type":"partial_transcript","text":"","turn_probability":0.1}`},
		{
			message: `{"message_type":"committed_transcript","text":"Hello, there."}`,
			want: []map[string]any{
				{"type": eventAudioCommitted},
				{"type": eventTranscriptCompleted, "transcript": "Hello, there."},
			},
		},
		{message: `{"message_type":"committed_transcript_with_timestamps","text":"Hello, there.","words":[{"text":"Hello","start":0.1,"end":0.3}]}`},
		{message: `{"message_type":"partial_transcript","text":"Next"}`, want: []map[string]any{{"type": eventTranscriptDelta, "delta": "Next"}}},
		{
			message: `{"message_type":"committed_transcript","text":"Next."}`,
			want: []map[string]any{
				{"type": eventAudioCommitted},
				{"type": eventTranscriptCompleted, "transcript": "Next."},
			},
		},
		{message: `{"message_type":"warning","warning":"slow down"}`},
		{message: `not json`},
	}

	var items []string // item id of every completed segment, in order
	var current string // item id the open segment's deltas carry
	for _, s := range steps {
		events := decodeEvents(t, tr.ProviderEvent([]byte(s.message)))
		require.Len(t, events, len(s.want), "message = %s", s.message)
		for i, want := range s.want {
			event := events[i]
			for key, value := range want {
				assert.Equal(t, value, event[key], "message = %s", s.message)
			}
			assert.NotEmpty(t, event["event_id"])
			itemID, _ := event["item_id"].(string)
			require.NotEmpty(t, itemID, "event = %v", event)
			if current == "" {
				current = itemID
			}
			assert.Equal(t, current, itemID, "every event of a segment carries its item id")

			switch event["type"] {
			case eventAudioCommitted:
				if len(items) == 0 {
					assert.Nil(t, event["previous_item_id"])
				} else {
					assert.Equal(t, items[len(items)-1], event["previous_item_id"])
				}
			case eventTranscriptCompleted:
				assert.EqualValues(t, 0, event["content_index"])
				items = append(items, itemID)
				current = ""
			case eventTranscriptDelta:
				assert.EqualValues(t, 0, event["content_index"])
			}
		}
	}
	require.Len(t, items, 2)
	assert.NotEqual(t, items[0], items[1])
}

func TestRealtimeTranslatorMapsErrors(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		wantType    string
		wantCode    string
		wantMessage string
	}{
		{
			name:        "bad provider key",
			message:     `{"message_type":"auth_error","error":"You must be authenticated to use this endpoint."}`,
			wantType:    "authentication_error",
			wantCode:    "auth_error",
			wantMessage: "You must be authenticated to use this endpoint.",
		},
		{
			name:        "quota",
			message:     `{"message_type":"quota_exceeded","error":"quota exceeded"}`,
			wantType:    "rate_limit_error",
			wantCode:    "quota_exceeded",
			wantMessage: "quota exceeded",
		},
		{
			name:        "commit throttled",
			message:     `{"message_type":"commit_throttled","error":"slow down"}`,
			wantType:    "rate_limit_error",
			wantCode:    "commit_throttled",
			wantMessage: "slow down",
		},
		{
			name:        "rejected audio",
			message:     `{"message_type":"input_error","error":"Message must be a valid protocol message"}`,
			wantType:    "invalid_request_error",
			wantCode:    "input_error",
			wantMessage: "Message must be a valid protocol message",
		},
		{
			name:        "transcriber failure",
			message:     `{"message_type":"transcriber_error","error":"boom"}`,
			wantType:    "provider_error",
			wantCode:    "transcriber_error",
			wantMessage: "boom",
		},
		{
			name:        "error type added later",
			message:     `{"message_type":"brand_new_error","error":"new"}`,
			wantType:    "provider_error",
			wantCode:    "brand_new_error",
			wantMessage: "new",
		},
		{
			name:        "structured error detail",
			message:     `{"message_type":"error","error":{"detail":"nested"}}`,
			wantType:    "provider_error",
			wantCode:    "error",
			wantMessage: `{"detail":"nested"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := newRealtimeTranslator("scribe_v2_realtime").ProviderEvent([]byte(tt.message))
			require.Len(t, frames, 1)

			event := decodeEvent(t, frames[0])
			assert.Equal(t, "error", event["type"])
			detail, ok := event["error"].(map[string]any)
			require.True(t, ok, "event = %v", event)
			assert.Equal(t, tt.wantType, detail["type"])
			assert.Equal(t, tt.wantCode, detail["code"])
			assert.Equal(t, tt.wantMessage, detail["message"])
			assert.Nil(t, detail["event_id"])
		})
	}
}
