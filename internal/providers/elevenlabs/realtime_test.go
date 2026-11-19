package elevenlabs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/realtime"
)

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "elevenlabs-secret-key"

	tests := []struct {
		name     string
		baseURL  string
		wantBase string
	}{
		{name: "default host", baseURL: "", wantBase: "wss://api.elevenlabs.io/v1/speech-to-text/realtime"},
		{
			name:     "data residency host",
			baseURL:  "https://api.eu.residency.elevenlabs.io",
			wantBase: "wss://api.eu.residency.elevenlabs.io/v1/speech-to-text/realtime",
		},
		{
			name:     "plain http base with a proxy prefix",
			baseURL:  "http://localhost:9000/elevenlabs/",
			wantBase: "ws://localhost:9000/elevenlabs/v1/speech-to-text/realtime",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider(apiKey, tt.baseURL, nil, llmclient.Hooks{})
			target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{
				Model:  " scribe_v2_realtime ",
				Intent: core.RealtimeIntentTranscription,
			})
			require.NoError(t, err)

			u, err := url.Parse(target.URL)
			require.NoError(t, err)
			query := u.Query()
			u.RawQuery = ""
			assert.Equal(t, tt.wantBase, u.String())
			assert.Equal(t, url.Values{
				"model_id":        {"scribe_v2_realtime"},
				"audio_format":    {"pcm_24000"},
				"commit_strategy": {"vad"},
			}, query)

			// ElevenLabs authenticates with its own header, not a bearer token.
			assert.Equal(t, apiKey, target.Headers.Get("xi-api-key"))
			assert.Empty(t, target.Headers.Get("Authorization"))
			// ElevenLabs reports no usage, so the relayed audio prices the session.
			assert.True(t, target.MeterInputAudio)
			assert.Empty(t, target.PinSessionModel)
			assert.NotNil(t, target.Translator)
			assert.NotNil(t, target.HTTPClient)
		})
	}
}

func TestRealtimeTargetReturnsAFreshTranslatorPerSession(t *testing.T) {
	// The translator holds one session's state, so two sessions must not share it.
	p := newTestProvider("key", "", nil, llmclient.Hooks{})
	first, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "scribe_v2_realtime"})
	require.NoError(t, err)
	second, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "scribe_v2_realtime"})
	require.NoError(t, err)
	assert.NotSame(t, first.Translator, second.Translator)
}

func TestRealtimeTargetRejectsInvalidRequests(t *testing.T) {
	p := newTestProvider("key", "", nil, llmclient.Hooks{})

	tests := []struct {
		name string
		req  *core.RealtimeRequest
		want string
	}{
		{name: "nil request", req: nil, want: "model is required"},
		{name: "blank model", req: &core.RealtimeRequest{Model: "  "}, want: "model is required"},
		{name: "call attach", req: &core.RealtimeRequest{Model: "scribe_v2_realtime", CallID: "rtc_1"}, want: "does not support realtime calls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := p.RealtimeTarget(context.Background(), tt.req)
			require.Error(t, err)
			assert.Nil(t, target)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestSupportsRealtimeIntent(t *testing.T) {
	p := newTestProvider("key", "", nil, llmclient.Hooks{})
	assert.True(t, p.SupportsRealtimeIntent(core.RealtimeIntentTranscription))
	assert.True(t, p.SupportsRealtimeIntent(" Transcription "))
	assert.False(t, p.SupportsRealtimeIntent(core.RealtimeIntentTranslation))
}

// scribeUpstream is a fake Scribe Realtime websocket. It speaks the ElevenLabs
// protocol: session_started on connect, a growing partial transcript for each
// audio chunk, a committed transcript for a commit, and a rejection that closes
// the socket for the chunk named "reject".
type scribeUpstream struct {
	*httptest.Server
	// handshakes records each websocket upgrade request.
	handshakes *providertest.Capture
	// received holds every frame the upstream read, valid or not, so a test can
	// tell that nothing but audio chunks reached it.
	received chan string
}

func newScribeUpstream(t *testing.T) *scribeUpstream {
	t.Helper()
	upstream := &scribeUpstream{received: make(chan string, 16)}
	upstream.Server, upstream.handshakes = providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		send := func(message string) bool {
			return conn.Write(ctx, websocket.MessageText, []byte(message)) == nil
		}
		if !send(`{"message_type":"session_started","session_id":"abc","config":{"language_code":null,"vad_threshold":0.4,"vad_silence_threshold_secs":1.5}}`) {
			return
		}
		words := []string{"Hello", "Hello there"}
		heard := 0
		for {
			_, frame, err := conn.Read(ctx)
			if err != nil {
				return
			}
			upstream.received <- string(frame)
			var chunk inputAudioChunk
			if json.Unmarshal(frame, &chunk) != nil || chunk.MessageType != "input_audio_chunk" {
				send(`{"message_type":"input_error","error":"Message must be a valid protocol message"}`)
				continue
			}
			switch {
			case chunk.AudioBase64 == "reject":
				send(`{"message_type":"invalid_request","error":"audio rejected"}`)
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid_request")
				return
			case chunk.Commit:
				// The real service sends the plain and the timestamped commit
				// when timestamps are on; the client must see one completion.
				send(`{"message_type":"committed_transcript","text":"Hello there."}`)
				send(`{"message_type":"committed_transcript_with_timestamps","text":"Hello there.","words":[]}`)
				heard = 0
			default:
				send(`{"message_type":"partial_transcript","text":"` + words[min(heard, len(words)-1)] + `"}`)
				heard++
			}
		}
	})
	return upstream
}

// openScribeSession relays a client through the gateway's realtime proxy to the
// fake upstream, using the target the provider builds.
func openScribeSession(t *testing.T, upstream *scribeUpstream) (*websocket.Conn, context.Context) {
	t.Helper()
	p := newTestProvider("elevenlabs-secret-key", upstream.URL, nil, llmclient.Hooks{})
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "scribe_v2_realtime"})
	require.NoError(t, err)

	done := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		_ = realtime.Proxy(w, r, realtime.Target{
			URL:        target.URL,
			Headers:    target.Headers,
			HTTPClient: target.HTTPClient,
		}, realtime.Hooks{Translator: target.Translator})
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.CloseNow()
		// The relay outlives the hijacked handler connection; wait for it so the
		// session cannot leak into the next test.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("realtime session did not end")
		}
		cancel()
		gateway.Close()
	})
	return client, ctx
}

func TestRealtimeSessionTranslatesBothWays(t *testing.T) {
	upstream := newScribeUpstream(t)
	client, ctx := openScribeSession(t, upstream)

	send := func(event string) {
		t.Helper()
		require.NoError(t, client.Write(ctx, websocket.MessageText, []byte(event)))
	}
	read := func() map[string]any {
		t.Helper()
		_, frame, err := client.Read(ctx)
		require.NoError(t, err)
		return decodeEvent(t, frame)
	}

	// The handshake carries the provider credential and the session settings.
	created := read()
	// The session started, so the handshake was recorded: it carries the
	// provider credential and the session settings.
	handshake := upstream.handshakes.Last(t)
	assert.Equal(t, "/v1/speech-to-text/realtime", handshake.Path)
	assert.Equal(t, "elevenlabs-secret-key", handshake.Header.Get("xi-api-key"))
	assert.Equal(t, "scribe_v2_realtime", handshake.Query.Get("model_id"))
	assert.Equal(t, "pcm_24000", handshake.Query.Get("audio_format"))
	assert.Equal(t, "vad", handshake.Query.Get("commit_strategy"))

	assert.Equal(t, "session.created", created["type"])
	session, ok := created["session"].(map[string]any)
	require.True(t, ok, "event = %v", created)
	assert.Equal(t, "transcription", session["type"])
	assert.Equal(t, "sess_abc", session["id"])

	// A session.update is answered by the gateway and never reaches ElevenLabs.
	send(`{"type":"session.update","session":{"type":"transcription"}}`)
	assert.Equal(t, "session.updated", read()["type"])

	send(`{"type":"input_audio_buffer.append","audio":"AAAA"}`)
	first := read()
	assert.Equal(t, eventTranscriptDelta, first["type"])
	assert.Equal(t, "Hello", first["delta"])
	assert.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"AAAA","commit":false,"sample_rate":24000}`, <-upstream.received)

	send(`{"type":"input_audio_buffer.append","audio":"BBBB"}`)
	second := read()
	assert.Equal(t, " there", second["delta"])
	assert.Equal(t, first["item_id"], second["item_id"])
	assert.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"BBBB","commit":false,"sample_rate":24000}`, <-upstream.received)

	send(`{"type":"input_audio_buffer.commit"}`)
	committed := read()
	assert.Equal(t, eventAudioCommitted, committed["type"])
	assert.Equal(t, first["item_id"], committed["item_id"])
	completed := read()
	assert.Equal(t, eventTranscriptCompleted, completed["type"])
	assert.Equal(t, "Hello there.", completed["transcript"])
	assert.Equal(t, first["item_id"], completed["item_id"])
	assert.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"","commit":true,"sample_rate":24000}`, <-upstream.received)

	// An event ElevenLabs has no counterpart for is answered, not forwarded.
	send(`{"type":"response.create","event_id":"evt_9"}`)
	assert.Equal(t, "error", read()["type"])

	// The timestamped duplicate of the commit is dropped, so the next event the
	// client reads is the next segment's first delta, under a new item. The
	// chunk behind it is the next frame the upstream read: neither the
	// session.update nor the response.create reached it.
	send(`{"type":"input_audio_buffer.append","audio":"CCCC"}`)
	next := read()
	assert.Equal(t, eventTranscriptDelta, next["type"])
	assert.Equal(t, "Hello", next["delta"])
	assert.NotEqual(t, first["item_id"], next["item_id"])
	assert.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"CCCC","commit":false,"sample_rate":24000}`, <-upstream.received)
}

func TestRealtimeSessionRelaysProviderErrorAndClose(t *testing.T) {
	// ElevenLabs reports a failure as a message and then closes the socket. The
	// client gets the failure as an OpenAI error event, then the same close.
	upstream := newScribeUpstream(t)
	client, ctx := openScribeSession(t, upstream)

	_, frame, err := client.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "session.created", decodeEvent(t, frame)["type"])

	require.NoError(t, client.Write(ctx, websocket.MessageText, []byte(`{"type":"input_audio_buffer.append","audio":"reject"}`)))

	_, frame, err = client.Read(ctx)
	require.NoError(t, err)
	event := decodeEvent(t, frame)
	assert.Equal(t, "error", event["type"])
	detail, ok := event["error"].(map[string]any)
	require.True(t, ok, "event = %v", event)
	assert.Equal(t, "invalid_request_error", detail["type"])
	assert.Equal(t, "invalid_request", detail["code"])
	assert.Equal(t, "audio rejected", detail["message"])

	_, _, err = client.Read(ctx)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
}
