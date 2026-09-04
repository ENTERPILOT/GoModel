package gemini

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
				if err == nil {
					t.Fatalf("expected invalid-request error, got parts %#v", parts)
				}
				if gatewayErr, ok := err.(*core.GatewayError); !ok || gatewayErr.Type != core.ErrorTypeInvalidRequest {
					t.Fatalf("error = %v, want invalid_request_error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("geminiPartsFromContentParts: %v", err)
			}
			if len(parts) != 2 || parts[1].InlineData == nil {
				t.Fatalf("parts = %#v, want text + inline_data", parts)
			}
			if parts[1].InlineData.MimeType != tc.wantMime || parts[1].InlineData.Data != tc.wantData {
				t.Errorf("inline_data = %+v, want mime %q data %q", *parts[1].InlineData, tc.wantMime, tc.wantData)
			}
		})
	}
}
