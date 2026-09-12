package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertResponsesContentToChatContent_NestedFileKeepsExtras(t *testing.T) {
	content, ok := ConvertResponsesContentToChatContent([]any{
		map[string]any{"type": "text", "text": "read"},
		map[string]any{
			"type":   "file",
			"x_part": true,
			"file": map[string]any{
				"file_id":  "file_123",
				"filename": "a.pdf",
				"x_file":   "keep",
			},
		},
		map[string]any{"type": "input_file", "file_url": "https://example.com/b.pdf", "filename": "b.pdf"},
	})
	require.True(t, ok)

	parts, isParts := content.([]core.ContentPart)
	require.True(t, isParts)
	require.Len(t, parts, 3)

	nested := parts[1]
	require.Equal(t, "file", nested.Type)
	require.NotNil(t, nested.File)
	require.Equal(t, "file_123", nested.File.FileID)
	require.Equal(t, "a.pdf", nested.File.Filename, "nested part = %+v", nested)
	got := string(nested.File.ExtraFields.Lookup("x_file"))
	assert.Equal(t, `"keep"`, got)
	got = string(nested.ExtraFields.Lookup("x_part"))
	assert.Equal(t, "true", got)
	assert.Empty(t, nested.ExtraFields.Lookup("x_file"))

	flat := parts[2]
	require.Equal(t, "file", flat.Type)
	require.NotNil(t, flat.File)
	require.Equal(t, "https://example.com/b.pdf", flat.File.FileURL)
	require.Empty(t, flat.File.FileData, "flat part = %+v", flat)
}

func TestBuildResponsesContentItemsFromParts_FileURLAndData(t *testing.T) {
	items := buildResponsesContentItemsFromParts([]core.ContentPart{
		{Type: "file", File: &core.FileContent{FileURL: "https://example.com/remote.pdf", Filename: "remote.pdf"}},
		{Type: "file", File: &core.FileContent{FileData: "data:application/pdf;base64,JVBERi0="}},
		{Type: "file", File: &core.FileContent{FileID: "file_123"}},
		{Type: "file", File: &core.FileContent{Filename: "empty.pdf"}},
	})
	require.Len(t, items, 3)
	assert.Equal(t, "input_file", items[0].Type)
	assert.Equal(t, "https://example.com/remote.pdf", items[0].FileURL)
	assert.Empty(t, items[0].FileData)
	assert.Equal(t, "remote.pdf", items[0].Filename, "remote item = %+v, want file_url only", items[0])
	assert.Equal(t, "data:application/pdf;base64,JVBERi0=", items[1].FileData)
	assert.Empty(t, items[1].FileURL, "inline item = %+v, want file_data only", items[1])
	assert.Equal(t, "file_123", items[2].FileID, "file id item = %+v", items[2])
}

func TestConvertResponsesContentToChatContent_TypedFileURLSurvives(t *testing.T) {
	content, ok := ConvertResponsesContentToChatContent([]core.ContentPart{
		{Type: "input_file", File: &core.FileContent{FileURL: " https://example.com/a.pdf ", Filename: "a.pdf"}},
	})
	require.True(t, ok)

	parts, isParts := content.([]core.ContentPart)
	require.True(t, isParts)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].File)
	require.Equal(t, "https://example.com/a.pdf", parts[0].File.FileURL, "content = %#v, want a file part keeping file_url", content)
}
