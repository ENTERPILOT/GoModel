package auditlog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsAudioContentType(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"audio/mpeg", true},
		{"audio/wav", true},
		{"audio/mpeg; charset=utf-8", true},
		{"AUDIO/MPEG", true},
		{"application/json", false},
		{"", false},
		{"text/plain", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, IsAudioContentType(tt.contentType), "IsAudioContentType(%q)", tt.contentType)
	}
}

func TestBuildAudioResponseBody_StoresMediaWhenCaptured(t *testing.T) {
	capture, store := newTestMediaCapture(t, 30)
	data := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	body := BuildAudioResponseBody(capture, "audio/mpeg", data)

	require.True(t, body.Audio)
	require.True(t, body.Stored)
	require.NotEmpty(t, body.MediaID)
	assert.Equal(t, int64(len(data)), body.Bytes)
	assert.Equal(t, "audio/mpeg", body.ContentType)

	_, stored := readMedia(t, store, body.MediaID)
	assert.Equal(t, data, stored)
}

func TestBuildAudioResponseBody_PlaceholderWithoutCapture(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02}
	body := BuildAudioResponseBody(nil, "audio/mpeg", data)

	require.True(t, body.Audio)
	assert.False(t, body.Stored)
	assert.Empty(t, body.MediaID, "no media should be stored without a capture: %+v", body)
	assert.Equal(t, int64(len(data)), body.Bytes)
}

func TestBuildAudioResponseBody_EmptyPayloadStoresNothing(t *testing.T) {
	capture, _ := newTestMediaCapture(t, 30)
	body := BuildAudioResponseBody(capture, "audio/mpeg", nil)
	assert.False(t, body.Stored)
	assert.Equal(t, int64(0), body.Bytes)
}

func TestBuildAudioUploadBody_StoresMediaAndMeta(t *testing.T) {
	capture, store := newTestMediaCapture(t, 30)
	meta := map[string]any{"model": "gpt-4o-transcribe", "filename": "a.mp3"}
	body := BuildAudioUploadBody(capture, "audio/mpeg", []byte("uploaded-audio"), meta)

	require.True(t, body.Audio)
	require.True(t, body.Stored)
	_, stored := readMedia(t, store, body.MediaID)
	require.Equal(t, "uploaded-audio", string(stored))
	assert.Equal(t, "gpt-4o-transcribe", body.Meta["model"], "meta not preserved alongside audio: %+v", body.Meta)
}

func TestBuildAudioUploadBody_PlaceholderKeepsMeta(t *testing.T) {
	meta := map[string]any{"model": "whisper-1"}
	body := BuildAudioUploadBody(nil, "audio/wav", []byte("x"), meta)

	assert.False(t, body.Stored)
	assert.Empty(t, body.MediaID, "no media should be stored without a capture: %+v", body)
	assert.Equal(t, "whisper-1", body.Meta["model"], "meta should be kept on the placeholder: %+v", body.Meta)
}
