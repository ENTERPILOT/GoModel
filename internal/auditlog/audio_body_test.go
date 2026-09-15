package auditlog

import (
	"encoding/base64"
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

func TestBuildAudioResponseBody_StoresBase64WhenEnabled(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	body := BuildAudioResponseBody("audio/mpeg", data, true)

	require.True(t, body.Audio)
	require.True(t, body.Stored)
	require.Equal(t, "base64", body.Encoding)
	assert.Equal(t, len(data), body.Bytes)

	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(decoded))
}

func TestBuildAudioResponseBody_PlaceholderWhenDisabled(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02}
	body := BuildAudioResponseBody("audio/mpeg", data, false)

	require.True(t, body.Audio)
	assert.False(t, body.Stored)
	assert.Empty(t, body.Data)
	assert.Empty(t, body.Encoding, "no bytes should be stored when disabled: %+v", body)
	assert.Equal(t, len(data), body.Bytes)
}

func TestBuildAudioUploadBody_StoresBase64AndMeta(t *testing.T) {
	data := []byte("uploaded-audio")
	meta := map[string]any{"model": "gpt-4o-transcribe", "filename": "a.mp3"}
	body := BuildAudioUploadBody("audio/mpeg", data, true, meta)

	require.True(t, body.Audio)
	require.True(t, body.Stored)
	require.Equal(t, "base64", body.Encoding)

	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	require.NoError(t, err)
	require.Equal(t, "uploaded-audio", string(decoded))
	assert.Equal(t, "gpt-4o-transcribe", body.Meta["model"], "meta not preserved alongside audio: %+v", body.Meta)
}

func TestBuildAudioUploadBody_PlaceholderKeepsMeta(t *testing.T) {
	meta := map[string]any{"model": "whisper-1"}
	body := BuildAudioUploadBody("audio/wav", []byte("x"), false, meta)

	assert.False(t, body.Stored)
	assert.Empty(t, body.Data, "no bytes should be stored when disabled: %+v", body)
	assert.Equal(t, "whisper-1", body.Meta["model"], "meta should be kept on the placeholder: %+v", body.Meta)
}

func TestBuildAudioResponseBody_TooLarge(t *testing.T) {
	data := make([]byte, audioBodyMaxBytes+1)
	body := BuildAudioResponseBody("audio/mpeg", data, true)

	assert.False(t, body.Stored)
	assert.Empty(t, body.Data)
	assert.True(t, body.TooLarge)
}

func TestBuildAudioResponseBody_AvoidsUTF8Corruption(t *testing.T) {
	// MP3 frame headers contain bytes that are invalid UTF-8; the old capture
	// path coerced these to U+FFFD. base64 must preserve them exactly.
	data := []byte{0xff, 0xfb, 0x90, 0x00}
	body := BuildAudioResponseBody("audio/mpeg", data, true)
	decoded, err := base64.StdEncoding.DecodeString(body.Data)
	require.NoError(t, err)

	// The bytes must survive verbatim — the old toValidUTF8String path would
	// have rewritten 0xff/0x90 into the U+FFFD replacement character (0xEF 0xBF
	// 0xBD), changing the byte length and content.
	require.Equal(t, data, decoded)
}
