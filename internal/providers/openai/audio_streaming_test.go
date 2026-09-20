package openai

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// trickleHandler answers with contentType and writes each chunk with a flush,
// holding the connection open until the caller has read the one before it. A
// handler that buffers the response cannot complete against it within the test's
// read deadline, so the assertions below are about relaying, not just bytes.
func trickleHandler(t *testing.T, contentType string, chunks ...string) (http.HandlerFunc, chan<- struct{}) {
	t.Helper()
	release := make(chan struct{}, len(chunks))
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				return
			}
		}
	}, release
}

// TestCreateSpeech_RelaysUpstreamBody pins that synthesized speech is handed
// back as a live stream: the provider must not wait for the whole generation, so
// the response carries Stream rather than buffered Data.
func TestCreateSpeech_RelaysUpstreamBody(t *testing.T) {
	handler, release := trickleHandler(t, "audio/mpeg", "first-", "second")
	provider, _ := newTestProvider(t, handler)

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "gpt-4o-mini-tts", Input: "hello", Voice: "alloy",
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Stream)
	assert.Empty(t, resp.Data, "a relayed body must not also be buffered")
	assert.Equal(t, "audio/mpeg", resp.ContentType)

	// The first chunk is readable while the upstream still holds the rest. A
	// single Read may return less than it, so the prefix is read in full.
	head := make([]byte, len("first-"))
	_, err = io.ReadFull(resp.Stream, head)
	require.NoError(t, err)
	assert.Equal(t, "first-", string(head))

	release <- struct{}{}
	release <- struct{}{}
	assert.Equal(t, "second", string(providertest.AudioBytes(t, resp)))
}

// TestCreateTranscription_StreamsOnlyWhenRequested covers both transcript paths:
// a forwarded stream=true is relayed as the upstream produces it, while a plain
// request stays buffered so providers layered on this adapter can still
// normalize the body.
func TestCreateTranscription_StreamsOnlyWhenRequested(t *testing.T) {
	const sse = "data: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\ndata: [DONE]\n\n"

	tests := []struct {
		name            string
		fields          []core.FormField
		upstreamType    string
		upstreamBody    string
		wantStream      bool
		wantContentType string
	}{
		{
			name:            "stream=true relays events",
			fields:          []core.FormField{{Name: "stream", Value: "true"}},
			upstreamType:    "text/event-stream",
			upstreamBody:    sse,
			wantStream:      true,
			wantContentType: "text/event-stream",
		},
		{
			// An upstream that ignores stream=true answers with a complete body;
			// the relay still describes it with the type the upstream sent, so
			// the client is not told to parse JSON as events.
			name:            "stream=true honored by a buffering upstream",
			fields:          []core.FormField{{Name: "stream", Value: "true"}},
			upstreamType:    "application/json",
			upstreamBody:    `{"text":"hi"}`,
			wantStream:      true,
			wantContentType: "application/json",
		},
		{
			name:            "no stream field stays buffered",
			upstreamType:    "application/json",
			upstreamBody:    `{"text":"hi"}`,
			wantStream:      false,
			wantContentType: "application/json",
		},
		{
			name:            "stream=false stays buffered",
			fields:          []core.FormField{{Name: "stream", Value: "false"}},
			upstreamType:    "application/json",
			upstreamBody:    `{"text":"hi"}`,
			wantStream:      false,
			wantContentType: "application/json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.upstreamType)
				_, _ = w.Write([]byte(tt.upstreamBody))
			})

			resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
				Model: "gpt-4o-transcribe", Filename: "speech.mp3", File: []byte("audio"), Fields: tt.fields,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantContentType, resp.ContentType)
			assert.Equal(t, tt.wantStream, resp.Stream != nil, "relayed body mismatch")
			assert.Equal(t, tt.upstreamBody, string(providertest.AudioBytes(t, resp)))
		})
	}
}

// TestCreateTranscription_StreamErrorIsMapped verifies a failed stream=true
// request still comes back as a gateway error rather than a relayed error body.
func TestCreateTranscription_StreamErrorIsMapped(t *testing.T) {
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"streaming is not supported","type":"invalid_request_error"}}`))
	})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model: "gpt-4o-transcribe", Filename: "speech.mp3", File: []byte("audio"),
		Fields: []core.FormField{{Name: "stream", Value: "true"}},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "streaming is not supported")
}
