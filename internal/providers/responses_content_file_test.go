package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
	if !ok {
		t.Fatal("conversion failed")
	}
	parts, isParts := content.([]core.ContentPart)
	if !isParts || len(parts) != 3 {
		t.Fatalf("content = %#v, want three parts", content)
	}
	nested := parts[1]
	if nested.Type != "file" || nested.File == nil || nested.File.FileID != "file_123" || nested.File.Filename != "a.pdf" {
		t.Fatalf("nested part = %+v", nested)
	}
	if got := string(nested.File.ExtraFields.Lookup("x_file")); got != `"keep"` {
		t.Errorf("nested file extra x_file = %s, want \"keep\"", got)
	}
	if got := string(nested.ExtraFields.Lookup("x_part")); got != "true" {
		t.Errorf("part extra x_part = %s, want true", got)
	}
	if len(nested.ExtraFields.Lookup("x_file")) != 0 {
		t.Errorf("file extra leaked onto the part: %s", nested.ExtraFields.Lookup("x_file"))
	}
	flat := parts[2]
	if flat.Type != "file" || flat.File == nil || flat.File.FileURL != "https://example.com/b.pdf" || flat.File.FileData != "" {
		t.Fatalf("flat part = %+v", flat)
	}
}

func TestBuildResponsesContentItemsFromParts_FileURLAndData(t *testing.T) {
	items := buildResponsesContentItemsFromParts([]core.ContentPart{
		{Type: "file", File: &core.FileContent{FileURL: "https://example.com/remote.pdf", Filename: "remote.pdf"}},
		{Type: "file", File: &core.FileContent{FileData: "data:application/pdf;base64,JVBERi0="}},
		{Type: "file", File: &core.FileContent{FileID: "file_123"}},
		{Type: "file", File: &core.FileContent{Filename: "empty.pdf"}},
	})
	if len(items) != 3 {
		t.Fatalf("items = %#v, want three input_file items", items)
	}
	if items[0].Type != "input_file" || items[0].FileURL != "https://example.com/remote.pdf" || items[0].FileData != "" || items[0].Filename != "remote.pdf" {
		t.Errorf("remote item = %+v, want file_url only", items[0])
	}
	if items[1].FileData != "data:application/pdf;base64,JVBERi0=" || items[1].FileURL != "" {
		t.Errorf("inline item = %+v, want file_data only", items[1])
	}
	if items[2].FileID != "file_123" {
		t.Errorf("file id item = %+v", items[2])
	}
}

func TestConvertResponsesContentToChatContent_TypedFileURLSurvives(t *testing.T) {
	content, ok := ConvertResponsesContentToChatContent([]core.ContentPart{
		{Type: "input_file", File: &core.FileContent{FileURL: " https://example.com/a.pdf ", Filename: "a.pdf"}},
	})
	if !ok {
		t.Fatal("conversion failed")
	}
	parts, isParts := content.([]core.ContentPart)
	if !isParts || len(parts) != 1 || parts[0].File == nil || parts[0].File.FileURL != "https://example.com/a.pdf" {
		t.Fatalf("content = %#v, want a file part keeping file_url", content)
	}
}
