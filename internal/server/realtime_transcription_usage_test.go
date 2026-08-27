package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/realtime"
	"github.com/enterpilot/gomodel/internal/usage"
)

// newTranscriptionSession opens a transcription session against an echoing
// upstream and returns the client socket plus the logger that captured its
// usage. The target mirrors a real OpenAI transcription target: the model is
// pinned in-session (the URL carries none) and the session is metered as a
// fallback for models that report no usage of their own.
func newTranscriptionSession(t *testing.T, upstream *realtimeUpstream) (*websocket.Conn, *usageCaptureLogger, func([]byte)) {
	t.Helper()
	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		realtimeTarget: &core.RealtimeTarget{
			URL:             "ws" + strings.TrimPrefix(upstream.URL, "http"),
			PinSessionModel: "gpt-4o-transcribe",
			MeterInputAudio: true,
		},
	}
	usageLogger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newRealtimeTestHandler(mock, usageLogger)

	e := echo.New()
	e.GET("/v1/realtime", handler.Realtime)
	gateway := httptest.NewServer(e)
	t.Cleanup(gateway.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/realtime?model=gpt-4o-transcribe&intent=transcription", nil)
	if err != nil {
		t.Fatalf("client dial failed: %v", err)
	}
	// Audio frames are far larger than the library default read limit; a real
	// realtime client raises it the same way the relay does.
	client.SetReadLimit(realtime.MaxFrameBytes)

	// send writes one frame and waits for the upstream echo. The echo proves the
	// relay already read — and therefore metered and tapped — the frame, so the
	// close that ends the session cannot outrun its accounting.
	send := func(frame []byte) {
		t.Helper()
		if err := client.Write(ctx, websocket.MessageText, frame); err != nil {
			t.Fatalf("client write failed: %v", err)
		}
		if _, _, err := client.Read(ctx); err != nil {
			t.Fatalf("client read failed: %v", err)
		}
	}
	return client, usageLogger, send
}

// realtimeAudioFrame builds an input audio append carrying seconds of PCM16
// mono at 24 kHz, the format realtime input defaults to.
func realtimeAudioFrame(t *testing.T, seconds int) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(make([]byte, seconds*24000*2)),
	})
	if err != nil {
		t.Fatalf("failed to build audio frame: %v", err)
	}
	return frame
}

func TestRealtimeTranscriptionWithoutUsageEventsRecordsAudioDuration(t *testing.T) {
	// A transcription session whose model reports no usage — no completed event,
	// or one without a usage object — would be recorded as free. The relayed
	// audio backs it instead: two seconds in, one duration entry out.
	upstream := newEchoingRealtimeUpstream(t)
	defer upstream.Close()

	client, usageLogger, send := newTranscriptionSession(t, upstream)
	frame := realtimeAudioFrame(t, 1)
	for range 2 {
		send(frame)
	}
	// Transcript deltas carry no usage, so they must not count as a report and
	// suppress the fallback.
	send([]byte(`{"type":"conversation.item.input_audio_transcription.delta","delta":"He"}`))
	_ = client.Close(websocket.StatusNormalClosure, "done")

	entries := waitForUsageEntries(t, usageLogger, 1)
	entry := entries[0]
	if entry.Endpoint != "/v1/realtime" {
		t.Errorf("endpoint = %q, want the realtime surface", entry.Endpoint)
	}
	if entry.Model != "gpt-4o-transcribe" {
		t.Errorf("model = %q, want the routed model", entry.Model)
	}
	seconds, ok := entry.RawData["audio_seconds"].(float64)
	if !ok {
		t.Fatalf("raw data = %v, want metered audio seconds", entry.RawData)
	}
	if math.Abs(seconds-2) > 1e-9 {
		t.Errorf("audio seconds = %v, want 2", seconds)
	}
}

func TestRealtimeTranscriptionReportingUsageIsNotMeteredTwice(t *testing.T) {
	// The common case: the model reports usage in its completed event, and that
	// report bills the session. Metering the relayed audio on top would bill the
	// same speech twice, so the fallback must stay out of the way.
	upstream := newEchoingRealtimeUpstream(t)
	defer upstream.Close()

	client, usageLogger, send := newTranscriptionSession(t, upstream)
	send(realtimeAudioFrame(t, 1))
	// Echoed back by the upstream, this reaches the gateway as the server event
	// a transcription session reports its usage in.
	send([]byte(`{
		"type": "conversation.item.input_audio_transcription.completed",
		"item_id": "item_1",
		"transcript": "Hello there.",
		"usage": {"type": "tokens", "total_tokens": 30, "input_tokens": 25, "output_tokens": 5}
	}`))
	_ = client.Close(websocket.StatusNormalClosure, "done")

	entries := waitForUsageEntries(t, usageLogger, 1)
	entry := entries[0]
	if entry.TotalTokens != 30 || entry.InputTokens != 25 {
		t.Errorf("tokens = (%d,%d), want the reported (25,30)", entry.InputTokens, entry.TotalTokens)
	}
	if _, metered := entry.RawData["audio_seconds"]; metered {
		t.Errorf("raw data = %v, want no metered duration for a session that reported usage", entry.RawData)
	}
}

func TestRealtimeTranscriptionWithoutAudioRecordsNothing(t *testing.T) {
	// A session that opened but relayed no audio produced nothing billable, so
	// the fallback must not write a zero-duration entry.
	upstream := newEchoingRealtimeUpstream(t)
	defer upstream.Close()

	client, usageLogger, send := newTranscriptionSession(t, upstream)
	send([]byte(`{"type":"session.update","session":{"type":"transcription"}}`))
	_ = client.Close(websocket.StatusNormalClosure, "done")

	select {
	case <-upstream.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session did not end")
	}
	if entries := usageLogger.Entries(); len(entries) != 0 {
		t.Errorf("usage entries = %d, want none for a session that relayed no audio", len(entries))
	}
}
