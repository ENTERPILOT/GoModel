package providers

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRealtimeURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		model    string
		wantBase string // scheme://host/path before query
		wantErr  bool
	}{
		{name: "openai https to wss", baseURL: "https://api.openai.com/v1", model: "gpt-realtime", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "xai https to wss", baseURL: "https://api.x.ai/v1", model: "grok-voice-latest", wantBase: "wss://api.x.ai/v1/realtime"},
		{name: "trailing slash normalized", baseURL: "https://api.openai.com/v1/", model: "m", wantBase: "wss://api.openai.com/v1/realtime"},
		{name: "http maps to ws", baseURL: "http://localhost:9000/v1", model: "m", wantBase: "ws://localhost:9000/v1/realtime"},
		{name: "ws preserved", baseURL: "ws://localhost:9000/v1", model: "m", wantBase: "ws://localhost:9000/v1/realtime"},
		{name: "wss preserved", baseURL: "wss://example.com/v1", model: "m", wantBase: "wss://example.com/v1/realtime"},
		{name: "empty base", baseURL: "", model: "m", wantErr: true},
		{name: "malformed url", baseURL: "http://[::1", model: "m", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://example.com/v1", model: "m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OpenAIRealtimeURL(tt.baseURL, tt.model)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)

			u, parseErr := url.Parse(got)
			require.NoError(t, parseErr)
			base := u.Scheme + "://" + u.Host + u.Path
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, tt.model, u.Query().Get("model"))
		})
	}
}

func TestOpenAIRealtimeURLTrimsModel(t *testing.T) {
	got, err := OpenAIRealtimeURL("https://api.openai.com/v1", "  gpt-realtime  ")
	require.NoError(t, err)

	u, _ := url.Parse(got)
	m := u.Query().Get("model")
	assert.Equal(t, "gpt-realtime", m)
}

func TestOpenAIRealtimeAttachURL(t *testing.T) {
	got, err := OpenAIRealtimeAttachURL("https://api.openai.com/v1", "rtc_123")
	require.NoError(t, err)

	u, _ := url.Parse(got)
	base := u.Scheme + "://" + u.Host + u.Path
	assert.Equal(t, "wss://api.openai.com/v1/realtime", base)
	id := u.Query().Get("call_id")
	assert.Equal(t, "rtc_123", id)
	_, present := // An attach targets an existing call, which already owns a model.
		u.Query()["model"]
	assert.False(t, present)
}

func TestOpenAIRealtimeAttachURLRequiresCallID(t *testing.T) {
	_, err := OpenAIRealtimeAttachURL("https://api.openai.com/v1", "  ")
	require.Error(t, err)
}

func TestOpenAIRealtimeHTTPURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		endpoint string
		want     string
		wantErr  bool
	}{
		{name: "calls endpoint", baseURL: "https://api.openai.com/v1", endpoint: "calls", want: "https://api.openai.com/v1/realtime/calls"},
		{name: "client secrets endpoint", baseURL: "https://api.openai.com/v1", endpoint: "client_secrets", want: "https://api.openai.com/v1/realtime/client_secrets"},
		{name: "trailing slash normalized", baseURL: "https://api.openai.com/v1/", endpoint: "/calls/", want: "https://api.openai.com/v1/realtime/calls"},
		{name: "wss maps back to https", baseURL: "wss://example.com/v1", endpoint: "calls", want: "https://example.com/v1/realtime/calls"},
		{name: "http preserved", baseURL: "http://localhost:9000/v1", endpoint: "calls", want: "http://localhost:9000/v1/realtime/calls"},
		{name: "empty endpoint is the realtime root", baseURL: "https://api.openai.com/v1", endpoint: "", want: "https://api.openai.com/v1/realtime"},
		{name: "empty base", baseURL: "", endpoint: "calls", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://example.com/v1", endpoint: "calls", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OpenAIRealtimeHTTPURL(tt.baseURL, tt.endpoint)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAIRealtimeTranslationURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		model    string
		wantBase string // scheme://host/path before query
		wantErr  bool
	}{
		{name: "openai https to wss", baseURL: "https://api.openai.com/v1", model: "gpt-realtime-translate", wantBase: "wss://api.openai.com/v1/realtime/translations"},
		{name: "trailing slash normalized", baseURL: "https://api.openai.com/v1/", model: "m", wantBase: "wss://api.openai.com/v1/realtime/translations"},
		{name: "compatible host preserved", baseURL: "http://localhost:9000/v1", model: "m", wantBase: "ws://localhost:9000/v1/realtime/translations"},
		{name: "empty base", baseURL: "", model: "m", wantErr: true},
		{name: "unsupported scheme", baseURL: "ftp://example.com/v1", model: "m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OpenAIRealtimeTranslationURL(tt.baseURL, tt.model)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)

			u, parseErr := url.Parse(got)
			require.NoError(t, parseErr)
			base := u.Scheme + "://" + u.Host + u.Path
			assert.Equal(t, tt.wantBase, base)
			got = // Unlike transcription sessions, translation sessions keep the model
				// in the URL.
				u.Query().Get("model")
			assert.Equal(t, tt.model, got)
		})
	}
}

func TestOpenAIRealtimeTranscriptionURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		// OpenAI rejects a model query parameter in transcription mode, so the
		// URL must carry intent=transcription and nothing else.
		{name: "openai https to wss", baseURL: "https://api.openai.com/v1", want: "wss://api.openai.com/v1/realtime?intent=transcription"},
		{name: "http maps to ws", baseURL: "http://localhost:9000/v1", want: "ws://localhost:9000/v1/realtime?intent=transcription"},
		{name: "empty base", baseURL: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OpenAIRealtimeTranscriptionURL(tt.baseURL)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)

			u, parseErr := url.Parse(got)
			require.NoError(t, parseErr)
			assert.False(t, u.Query().Has("model"), "url %q carries a model parameter; transcription sessions must not send one", got)
		})
	}
}
