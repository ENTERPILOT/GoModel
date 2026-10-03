package openai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestAdaptInlineFiles(t *testing.T) {
	notes := "data:text/plain;base64,VGhlIHZhdWx0IHBhc3N3b3JkIGlzIEtJV0ktMTE5OS4=" // "The vault password is KIWI-1199."
	tests := []struct {
		name string
		file core.FileContent
		want core.ContentPart
	}{
		{name: "named text file becomes headed text",
			file: core.FileContent{FileData: notes, Filename: "notes.txt"},
			want: core.ContentPart{Type: "text", Text: "notes.txt\n\nThe vault password is KIWI-1199."}},
		{name: "default-named text file becomes plain text",
			file: core.FileContent{FileData: notes, Filename: "document.txt"},
			want: core.ContentPart{Type: "text", Text: "The vault password is KIWI-1199."}},
		{name: "markdown counts as text",
			file: core.FileContent{FileData: "data:text/markdown;base64,IyBUaXRsZQ=="},
			want: core.ContentPart{Type: "text", Text: "# Title"}},
		{name: "unnamed PDF gets a name",
			file: core.FileContent{FileData: "data:application/pdf;base64,JVBERi0="},
			want: core.ContentPart{Type: "file", File: &core.FileContent{FileData: "data:application/pdf;base64,JVBERi0=", Filename: "document.pdf"}}},
		{name: "undecodable text stays a file",
			file: core.FileContent{FileData: "data:text/plain;base64,%%%", Filename: "x.txt"},
			want: core.ContentPart{Type: "file", File: &core.FileContent{FileData: "data:text/plain;base64,%%%", Filename: "x.txt"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := tt.file
			req := &core.ChatRequest{Model: "gpt-6-luna", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
				{Type: "file", File: &file}, {Type: "text", Text: "question"},
			}}}}
			got := adaptInlineFiles(req)
			parts := got.Messages[0].Content.([]core.ContentPart)
			require.Len(t, parts, 2)
			assert.Equal(t, tt.want, parts[0])
			assert.Equal(t, tt.file, file, "the caller's file must not change")
			assert.Equal(t, &file, req.Messages[0].Content.([]core.ContentPart)[0].File, "the caller's parts must not change")
		})
	}
}

// Text files reach Chat Completions as text, which accepts only PDF file data.
func TestChatCompletion_SendsTextFilesAsText(t *testing.T) {
	provider, capture := newRoutingProvider(t)
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.5", Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
		{Type: "file", File: &core.FileContent{FileData: "data:text/plain;base64,aGVsbG8=", Filename: "a.txt"}},
	}}}})
	require.NoError(t, err)
	sent := capture.Last(t)
	assert.Equal(t, "/chat/completions", sent.Path)
	parts := sent.JSON(t)["messages"].([]any)[0].(map[string]any)["content"].([]any)
	assert.Equal(t, map[string]any{"type": "text", "text": "a.txt\n\nhello"}, parts[0])
}
