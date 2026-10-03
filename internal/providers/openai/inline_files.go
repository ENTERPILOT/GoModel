package openai

import (
	"encoding/base64"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// adaptInlineFiles prepares inline file parts for Chat Completions, which
// requires a filename alongside file_data and accepts only PDF data:
//   - a file without a filename gets the default name for its media type;
//   - a plain-text file (text/* data) becomes a text part carrying its
//     content, headed by its filename when the caller named it.
//
// The caller's request is left unchanged; it is returned as-is when no part
// needs adapting.
func adaptInlineFiles(req *core.ChatRequest) *core.ChatRequest {
	adapted := req
	for i, msg := range req.Messages {
		parts, ok := msg.Content.([]core.ContentPart)
		if !ok || !slices.ContainsFunc(parts, needsFileAdapting) {
			continue
		}
		rewritten := slices.Clone(parts)
		for j, part := range rewritten {
			if needsFileAdapting(part) {
				rewritten[j] = adaptFilePart(part)
			}
		}
		if adapted == req {
			cloned := *req
			cloned.Messages = slices.Clone(req.Messages)
			adapted = &cloned
		}
		adapted.Messages[i].Content = rewritten
	}
	return adapted
}

// needsFileAdapting reports whether a part is inline file data without a
// filename or with a text media type.
func needsFileAdapting(part core.ContentPart) bool {
	if part.Type != "file" || part.File == nil || part.File.FileData == "" {
		return false
	}
	_, isText := inlineText(part.File.FileData)
	return part.File.Filename == "" || isText
}

// adaptFilePart applies adaptInlineFiles to one part.
func adaptFilePart(part core.ContentPart) core.ContentPart {
	file := *part.File
	defaultName := core.DefaultFilename(file.FileData)
	if text, ok := inlineText(file.FileData); ok {
		if name := strings.TrimSpace(file.Filename); name != "" && name != defaultName {
			text = name + "\n\n" + text
		}
		return core.ContentPart{Type: "text", Text: text, ExtraFields: part.ExtraFields}
	}
	if file.Filename == "" {
		file.Filename = defaultName
	}
	part.File = &file
	return part
}

// inlineText decodes a base64 data URL with a text/* media type.
func inlineText(fileData string) (string, bool) {
	header, data, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(fileData), "data:"), ",")
	if !ok || !strings.HasPrefix(strings.ToLower(header), "text/") || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
