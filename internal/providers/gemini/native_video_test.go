package gemini

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiPartsFromContentParts_VideoProjection(t *testing.T) {
	tests := []struct {
		name        string
		video       core.VideoURLContent
		wantMime    string
		wantData    string
		wantFileURI string
		wantErr     bool
	}{
		{
			name:     "inline mp4 data url",
			video:    core.VideoURLContent{URL: "data:video/mp4;base64,AAAAIGZ0", Detail: "low"},
			wantMime: "video/mp4",
			wantData: "AAAAIGZ0",
		},
		{
			name:     "data url without media type defaults to mp4",
			video:    core.VideoURLContent{URL: "data:;base64,AAAAIGZ0"},
			wantMime: "video/mp4",
			wantData: "AAAAIGZ0",
		},
		{
			name:        "youtube link",
			video:       core.VideoURLContent{URL: "https://www.youtube.com/watch?v=abc123"},
			wantFileURI: "https://www.youtube.com/watch?v=abc123",
		},
		{
			name:        "short youtube link",
			video:       core.VideoURLContent{URL: "https://youtu.be/abc123"},
			wantFileURI: "https://youtu.be/abc123",
		},
		{
			name:        "files api uri",
			video:       core.VideoURLContent{URL: "https://generativelanguage.googleapis.com/v1beta/files/abc123"},
			wantFileURI: "https://generativelanguage.googleapis.com/v1beta/files/abc123",
		},
		{
			name:        "cloud storage uri",
			video:       core.VideoURLContent{URL: "gs://bucket/clip.mp4"},
			wantFileURI: "gs://bucket/clip.mp4",
		},
		{name: "remote url", video: core.VideoURLContent{URL: "https://example.com/clip.mp4"}, wantErr: true},
		{
			name:    "lookalike files api host",
			video:   core.VideoURLContent{URL: "https://example.com/generativelanguage.googleapis.com/v1beta/files/clip.mp4"},
			wantErr: true,
		},
		{
			name:    "files api host as subdomain suffix",
			video:   core.VideoURLContent{URL: "https://generativelanguage.googleapis.com.example.com/v1beta/files/clip.mp4"},
			wantErr: true,
		},
		{
			name:    "lookalike youtube host",
			video:   core.VideoURLContent{URL: "https://youtube.com.example.com/watch?v=abc123"},
			wantErr: true,
		},
		{
			name:    "files api over http",
			video:   core.VideoURLContent{URL: "http://generativelanguage.googleapis.com/v1beta/files/abc123"},
			wantErr: true,
		},
		{
			name:    "files api host without files path",
			video:   core.VideoURLContent{URL: "https://generativelanguage.googleapis.com/v1beta/models/gemini"},
			wantErr: true,
		},
		{name: "malformed data url", video: core.VideoURLContent{URL: "data:video/mp4;base64"}, wantErr: true},
		{name: "blank url", video: core.VideoURLContent{URL: "   "}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			video := tc.video
			parts, err := geminiPartsFromContentParts([]core.ContentPart{
				{Type: "text", Text: "describe"},
				{Type: "video_url", VideoURL: &video},
			})
			if tc.wantErr {
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
				return
			}
			require.NoError(t, err)
			require.Len(t, parts, 2)
			assert.Equal(t, "describe", parts[0].Text)
			if tc.wantFileURI != "" {
				require.NotNil(t, parts[1].FileData)
				assert.Equal(t, tc.wantFileURI, parts[1].FileData.FileURI)
				return
			}
			require.NotNil(t, parts[1].InlineData)
			assert.Equal(t, tc.wantMime, parts[1].InlineData.MimeType)
			assert.Equal(t, tc.wantData, parts[1].InlineData.Data)
		})
	}
}

func TestGeminiPartsFromContentParts_MissingVideoURL(t *testing.T) {
	_, err := geminiPartsFromContentParts([]core.ContentPart{{Type: "video_url"}})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

func TestGeminiPartsFromContentParts_UnsupportedPartTypeRejected(t *testing.T) {
	_, err := geminiPartsFromContentParts([]core.ContentPart{{Type: "input_video", Text: "x"}})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}
