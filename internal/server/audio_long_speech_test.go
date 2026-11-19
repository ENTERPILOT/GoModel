package server

import (
	"context"
	"encoding/binary"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/internal/usage"
)

// longSpeechInput splits into three sentences at a 25-character limit.
const longSpeechInput = "First sentence here. Second sentence here. Third one."

var longSpeechParts = []string{"First sentence here.", "Second sentence here.", "Third one."}

// partSpeechProvider answers every speech call with audio of its own and
// records the request each one carried.
type partSpeechProvider struct {
	*audioMockProvider
	respond  func(call int) (*core.AudioResponse, error)
	requests []*core.AudioSpeechRequest
}

func (p *partSpeechProvider) CreateSpeech(_ context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	p.requests = append(p.requests, req)
	return p.respond(len(p.requests) - 1)
}

func (p *partSpeechProvider) inputs() []string {
	inputs := make([]string, len(p.requests))
	for i, req := range p.requests {
		inputs[i] = req.Input
	}
	return inputs
}

// newPartSpeechProvider returns a provider that answers each call with half a
// second of 24 kHz mono 16-bit WAV, unless respond says otherwise.
func newPartSpeechProvider(respond func(call int) (*core.AudioResponse, error)) *partSpeechProvider {
	if respond == nil {
		respond = func(int) (*core.AudioResponse, error) {
			return &core.AudioResponse{ContentType: "audio/wav", Data: wavBytes(24000, 1, 16, 0.5)}, nil
		}
	}
	return &partSpeechProvider{
		audioMockProvider: &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}},
		respond:           respond,
	}
}

func longSpeechBody(extra string) string {
	return `{"model":"gpt-4o-mini-tts","input":"` + longSpeechInput + `","voice":"alloy"` + extra + `}`
}

// TestAudioSpeech_LongInputIsSplitAndJoined covers the whole path: input over
// the limit goes upstream sentence by sentence with the caller's parameters,
// comes back as one WAV file, and is metered and logged as the one request it
// is to the client.
func TestAudioSpeech_LongInputIsSplitAndJoined(t *testing.T) {
	provider := newPartSpeechProvider(nil)
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	media, store := newTestMediaCapturer(t)
	svc := &audioService{
		provider:         provider,
		usageLogger:      usageLogger,
		pricingResolver:  &mockPricingResolver{pricing: &core.ModelPricing{PerSecondOutput: new(0.00025)}},
		logBodies:        true,
		logAudioBodies:   true,
		media:            media,
		speechChunkChars: 25,
	}
	entry := &requestlog.LogEntry{}
	c, rec := echotest.Post(t, "/v1/audio/speech", longSpeechBody(`,"response_format":"wav","speed":1.25,"seed":7`),
		echotest.WithValue(string(requestlog.LogEntryKey), entry))

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, longSpeechParts, provider.inputs())
	for _, req := range provider.requests {
		assert.Equal(t, "gpt-4o-mini-tts", req.Model)
		assert.Equal(t, "alloy", req.Voice)
		assert.Equal(t, "wav", req.ResponseFormat)
		assert.InDelta(t, 1.25, req.Speed, 0)
		assert.JSONEq(t, "7", string(req.ExtraFields.Lookup("seed")), "forwarded parameters go with every part")
	}

	// One WAV file: a single header that leaves the length open, then the
	// samples of all three parts.
	const partSamples = 24000
	body := rec.Body.Bytes()
	assert.Equal(t, "audio/wav", rec.Header().Get("Content-Type"))
	require.Len(t, body, 44+3*partSamples)
	assert.Equal(t, "RIFF", string(body[0:4]))
	assert.Equal(t, uint32(0xFFFFFFFF), binary.LittleEndian.Uint32(body[4:8]))
	assert.Equal(t, "data", string(body[36:40]))
	assert.Equal(t, uint32(0xFFFFFFFF), binary.LittleEndian.Uint32(body[40:44]))
	assert.NotContains(t, string(body[44:]), "RIFF", "a later part's header leaked into the audio")

	// One usage entry for the request: the characters sent upstream and the
	// duration of all the audio.
	require.Len(t, usageLogger.entries, 1)
	logged := usageLogger.entries[0]
	assert.Equal(t, len("First sentence here.Second sentence here.Third one."), logged.RawData["input_characters"])
	assert.InDelta(t, 1.5, logged.RawData["audio_output_seconds"], 0.001)
	require.NotNil(t, logged.TotalCost)
	assert.InDelta(t, 1.5*0.00025, *logged.TotalCost, 1e-9)
	assert.Empty(t, logged.CostsCalculationCaveat)

	// One request log entry: the input as the client sent it, the audio as the
	// client received it.
	reqBody, ok := entry.Data.RequestBody.(map[string]any)
	require.True(t, ok, "request body not captured as map, got %T", entry.Data.RequestBody)
	assert.Equal(t, longSpeechInput, reqBody["input"])
	respBody, ok := entry.Data.ResponseBody.(requestlog.AudioBodyLog)
	require.True(t, ok, "response body not captured as audio, got %T", entry.Data.ResponseBody)
	assert.Equal(t, int64(len(body)), respBody.Bytes)
	assert.Equal(t, body, readTestMedia(t, store, respBody.MediaID))
	assert.Empty(t, entry.ErrorType)
}

// TestAudioSpeech_InputForwardedWhole lists what must reach the provider as
// one unchanged request even when the input is over the limit.
func TestAudioSpeech_InputForwardedWhole(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		body  string
	}{
		{name: "splitting is off by default", limit: 0, body: longSpeechBody(`,"response_format":"wav"`)},
		{name: "input within the limit", limit: len(longSpeechInput), body: longSpeechBody(`,"response_format":"wav"`)},
		{name: "default format is mp3", limit: 25, body: longSpeechBody("")},
		{name: "mp3 cannot be joined", limit: 25, body: longSpeechBody(`,"response_format":"mp3"`)},
		{name: "opus cannot be joined", limit: 25, body: longSpeechBody(`,"response_format":"opus"`)},
		{name: "server-sent events are not audio bytes", limit: 25, body: longSpeechBody(`,"response_format":"pcm","stream_format":"sse"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newPartSpeechProvider(func(int) (*core.AudioResponse, error) {
				return &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")}, nil
			})
			svc := &audioService{provider: provider, speechChunkChars: tt.limit}
			c, rec := echotest.Post(t, "/v1/audio/speech", tt.body)

			require.NoError(t, svc.CreateSpeech(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, []string{longSpeechInput}, provider.inputs())
			assert.Equal(t, "synthetic-audio", rec.Body.String())
		})
	}
}

// TestAudioSpeech_LongInputPCMAndExplicitAudioStream: pcm is joined too, and
// naming the default stream_format does not turn splitting off.
func TestAudioSpeech_LongInputPCMAndExplicitAudioStream(t *testing.T) {
	provider := newPartSpeechProvider(func(call int) (*core.AudioResponse, error) {
		return &core.AudioResponse{ContentType: "audio/pcm", Data: []byte{byte('a' + call), 0}}, nil
	})
	svc := &audioService{provider: provider, speechChunkChars: 25}
	c, rec := echotest.Post(t, "/v1/audio/speech", longSpeechBody(`,"response_format":"pcm","stream_format":"audio"`))

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, longSpeechParts, provider.inputs())
	assert.Equal(t, "audio/pcm", rec.Header().Get("Content-Type"))
	assert.Equal(t, "a\x00b\x00c\x00", rec.Body.String())
}

// TestAudioSpeech_LongInputRejectedBeforeAnyAudio: the first part is requested
// before the response starts, so a provider that refuses the request still
// produces an ordinary error response, and nothing is metered.
func TestAudioSpeech_LongInputRejectedBeforeAnyAudio(t *testing.T) {
	provider := newPartSpeechProvider(func(int) (*core.AudioResponse, error) {
		return nil, core.NewRateLimitError("openai", "slow down")
	})
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	svc := &audioService{provider: provider, usageLogger: usageLogger, speechChunkChars: 25}
	c, rec := echotest.Post(t, "/v1/audio/speech", longSpeechBody(`,"response_format":"wav"`))

	require.NoError(t, svc.CreateSpeech(c))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "slow down")
	assert.Len(t, provider.requests, 1)
	assert.Empty(t, usageLogger.entries)
}

// TestAudioSpeech_LongInputLaterPartFails: once audio is flowing the status
// is sent, so a part that fails ends the audio there. What was synthesized is
// still metered, and the request log entry records the failure.
func TestAudioSpeech_LongInputLaterPartFails(t *testing.T) {
	provider := newPartSpeechProvider(func(call int) (*core.AudioResponse, error) {
		if call == 1 {
			return nil, core.NewRateLimitError("openai", "slow down")
		}
		return &core.AudioResponse{ContentType: "audio/wav", Data: wavBytes(24000, 1, 16, 0.5)}, nil
	})
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	svc := &audioService{provider: provider, usageLogger: usageLogger, speechChunkChars: 25}
	entry := &requestlog.LogEntry{}
	c, rec := echotest.Post(t, "/v1/audio/speech", longSpeechBody(`,"response_format":"wav"`),
		echotest.WithValue(string(requestlog.LogEntryKey), entry))

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 44+24000, rec.Body.Len(), "the first part's audio reaches the client")
	assert.Len(t, provider.requests, 2, "no part is requested after the one that failed")

	require.Len(t, usageLogger.entries, 1)
	assert.InDelta(t, 0.5, usageLogger.entries[0].RawData["audio_output_seconds"], 0.001)
	assert.Equal(t, "stream_error", entry.ErrorType)
	require.NotNil(t, entry.Data)
	assert.Contains(t, entry.Data.ErrorMessage, "slow down")
}
