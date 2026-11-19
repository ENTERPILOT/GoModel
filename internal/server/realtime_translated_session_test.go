package server

import (
	"context"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/realtime"
	"github.com/enterpilot/gomodel/internal/usage"
)

// nativeProtocolTranslator stands in for a provider with its own websocket
// protocol: audio appends become a native chunk message, and every native
// message comes back as a completed transcript without usage.
type nativeProtocolTranslator struct{}

func (nativeProtocolTranslator) ClientEvent([]byte) (upstream, replies [][]byte) {
	return [][]byte{[]byte(`{"message_type":"chunk"}`)}, nil
}

func (nativeProtocolTranslator) ProviderEvent([]byte) [][]byte {
	return [][]byte{[]byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello there."}`)}
}

func TestRealtimeTranslatedSessionRecordsAudioDuration(t *testing.T) {
	// A provider behind a translator reports no usage in the OpenAI schema, so
	// its session is billed from the audio the client streamed. The meter reads
	// the client's own events, before translation turns them into the provider's
	// protocol, and the translated transcript — which carries no usage — must
	// not count as a report that suppresses it.
	upstream := newEchoingRealtimeUpstream(t)
	defer upstream.Close()

	mock := &realtimeWebRTCMock{
		mockProvider: &mockProvider{supportedModels: []string{"scribe_v2_realtime"}},
		realtimeTarget: &core.RealtimeTarget{
			URL:             "ws" + strings.TrimPrefix(upstream.URL, "http"),
			MeterInputAudio: true,
			Translator:      nativeProtocolTranslator{},
		},
	}
	usageLogger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	handler := newRealtimeTestHandler(mock, usageLogger)

	e := echo.New()
	e.GET("/v1/realtime", handler.Realtime)
	gateway := httptest.NewServer(e)
	defer gateway.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/realtime?model=scribe_v2_realtime", nil)
	require.NoError(t, err)
	client.SetReadLimit(realtime.MaxFrameBytes)

	frame := realtimeAudioFrame(t, 1)
	for range 2 {
		require.NoError(t, client.Write(ctx, websocket.MessageText, frame))
		// The upstream echoes the native chunk, and the client receives it
		// translated back: proof the frame passed the meter and both directions
		// of the translator.
		_, reply, err := client.Read(ctx)
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello there."}`, string(reply))
	}
	_ = client.Close(websocket.StatusNormalClosure, "done")

	entries := waitForUsageEntries(t, usageLogger, 1)
	entry := entries[0]
	assert.Equal(t, "/v1/realtime", entry.Endpoint)
	assert.Equal(t, "scribe_v2_realtime", entry.Model)

	seconds, ok := entry.RawData["audio_seconds"].(float64)
	require.True(t, ok, "raw data = %v, want metered audio seconds", entry.RawData)
	assert.LessOrEqual(t, math.Abs(seconds-2), 1e-9, "audio seconds = %v, want 2", seconds)
}
