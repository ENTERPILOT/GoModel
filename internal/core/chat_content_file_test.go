package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentPartFileRoundTrip(t *testing.T) {
	raw := `{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0=","filename":"report.pdf"},"cache_control":{"type":"ephemeral"}}`
	var part ContentPart
	err := json.Unmarshal([]byte(raw), &part)
	require.NoError(t, err)
	require.Equal(t, "file", part.Type)
	require.NotNil(t, part.File)
	require.Equal(t, "data:application/pdf;base64,JVBERi0=", part.File.FileData)
	require.Equal(t, "report.pdf", part.File.Filename, "part = %+v", part)
	got := string(part.ExtraFields.Lookup("cache_control"))
	assert.Equal(t, `{"type":"ephemeral"}`, got)

	encoded, err := json.Marshal(part)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(encoded, &decoded)
	require.NoError(t, err)

	file, _ := decoded["file"].(map[string]any)
	assert.Equal(t, "file", decoded["type"])
	assert.Equal(t, "data:application/pdf;base64,JVBERi0=", file["file_data"])
	assert.Equal(t, "report.pdf", file["filename"], "encoded = %s", encoded)
	_, ok := decoded["cache_control"]
	assert.True(t, ok, "encoded lost cache_control: %s", encoded)
}

func TestContentPartFileRequiresPayload(t *testing.T) {
	for _, raw := range []string{
		`{"type":"file"}`,
		`{"type":"file","file":{}}`,
		`{"type":"file","file":{"filename":"x.pdf"}}`,
	} {
		var part ContentPart
		assert.Error(t, json.Unmarshal([]byte(raw), &part), "Unmarshal(%s) = nil error, want payload error", raw)
	}
	var part ContentPart
	err := json.Unmarshal([]byte(`{"type":"input_file","file":{"file_id":"file_123"}}`), &part)
	require.NoError(t, err)
	assert.Equal(t, "file", part.Type)
	assert.Equal(t, "file_123", part.File.FileID, "part = %+v", part)
}

func TestNormalizeMessageContentFilePart(t *testing.T) {
	normalized, err := NormalizeMessageContent([]any{
		map[string]any{"type": "text", "text": "read this"},
		map[string]any{"type": "file", "file": map[string]any{"file_id": "file_123", "filename": "a.pdf"}},
	})
	require.NoError(t, err)

	parts, ok := normalized.([]ContentPart)
	require.True(t, ok)
	require.Len(t, parts, 2)
	require.Equal(t, "file", parts[1].Type)
	require.NotNil(t, parts[1].File)
	require.Equal(t, "file_123", parts[1].File.FileID, "normalized = %#v", normalized)
	assert.Equal(t, "read this", ExtractTextContent(parts))
}

func TestContentPartFileURLRoundTrip(t *testing.T) {
	raw := `{"type":"input_file","file":{"file_url":"https://example.com/a.pdf","filename":"a.pdf","x_file":1}}`
	var part ContentPart
	err := json.Unmarshal([]byte(raw), &part)
	require.NoError(t, err)
	require.Equal(t, "file", part.Type)
	require.NotNil(t, part.File)
	require.Equal(t, "https://example.com/a.pdf", part.File.FileURL)
	require.Empty(t, part.File.FileData, "part = %+v", part)
	got := string(part.File.ExtraFields.Lookup("x_file"))
	assert.Equal(t, "1", got)

	encoded, err := json.Marshal(part)
	require.NoError(t, err)

	var decoded struct {
		File map[string]any `json:"file"`
	}
	err = json.Unmarshal(encoded, &decoded)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/a.pdf", decoded.File["file_url"])
	assert.Equal(t, float64(1), decoded.File["x_file"], "encoded = %s", encoded)
	_, ok := decoded.File["file_data"]
	assert.False(t, ok, "encoded emitted empty file_data: %s", encoded)
}
