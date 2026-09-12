package gemini

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiPartsFromContentParts_FileProjection(t *testing.T) {
	tests := []struct {
		name     string
		file     core.FileContent
		wantMime string
		wantData string
		wantErr  bool
	}{
		{
			name:     "inline pdf data url",
			file:     core.FileContent{FileData: "data:application/pdf;base64,JVBERi0=", Filename: "a.pdf"},
			wantMime: "application/pdf",
			wantData: "JVBERi0=",
		},
		{name: "malformed data url", file: core.FileContent{FileData: "data:application/pdf;base64"}, wantErr: true},
		{name: "remote url", file: core.FileContent{FileURL: "https://example.com/a.pdf"}, wantErr: true},
		{name: "remote url in file_data", file: core.FileContent{FileData: "https://example.com/a.pdf"}, wantErr: true},
		{name: "file id", file: core.FileContent{FileID: "file_123"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.file
			parts, err := geminiPartsFromContentParts([]core.ContentPart{
				{Type: "text", Text: "read"},
				{Type: "file", File: &file},
			})
			if tc.wantErr {
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
				return
			}
			require.NoError(t, err)
			require.Len(t, parts, 2)
			require.NotNil(t, parts[1].InlineData)
			assert.Equal(t, tc.wantMime, parts[1].InlineData.MimeType)
			assert.Equal(t, tc.wantData, parts[1].InlineData.Data)
		})
	}
}
