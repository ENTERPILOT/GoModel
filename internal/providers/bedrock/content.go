package bedrock

import (
	"encoding/base64"
	"mime"
	"slices"
	"strings"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/enterpilot/gomodel/internal/core"
)

// convertContent maps user or tool message content onto Converse blocks. Text
// only content stays a single text block (none when empty); content with images
// keeps its parts in order. Parts the adapter cannot forward are rejected, as
// dropping them would change the result (ADR-0011).
func convertContent(content any) ([]brtypes.ContentBlock, error) {
	var parts []core.ContentPart
	if core.HasStructuredContent(content) {
		normalized, err := core.NormalizeMessageContent(content)
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid bedrock message content: "+err.Error(), err)
		}
		parts, _ = normalized.([]core.ContentPart)
	}
	if !slices.ContainsFunc(parts, func(part core.ContentPart) bool { return part.Type != "text" && part.Type != "input_text" }) {
		text := core.ExtractTextContent(content)
		if text == "" {
			return nil, nil
		}
		return []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: text}}, nil
	}

	blocks := make([]brtypes.ContentBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: part.Text})
			}
		case "image_url":
			image, err := convertImage(part.ImageURL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, &brtypes.ContentBlockMemberImage{Value: image})
		default:
			return nil, core.NewInvalidRequestError("bedrock chat does not support "+part.Type+" content", nil)
		}
	}
	return blocks, nil
}

// convertImage decodes a base64 data: URL into a Converse image block. Converse
// takes image bytes (or an S3 location), so remote URLs cannot be forwarded.
func convertImage(image *core.ImageURLContent) (brtypes.ImageBlock, error) {
	if image == nil || !strings.HasPrefix(image.URL, "data:") {
		return brtypes.ImageBlock{}, core.NewInvalidRequestError("bedrock chat image_url must be a base64 data: URL", nil)
	}
	header, data, ok := strings.Cut(strings.TrimPrefix(image.URL, "data:"), ",")
	meta, isBase64 := strings.CutSuffix(header, ";base64")
	if !ok || !isBase64 || data == "" {
		return brtypes.ImageBlock{}, core.NewInvalidRequestError("bedrock chat image_url must be a base64 data: URL", nil)
	}
	if meta == "" {
		meta = image.MediaType
	}
	mediaType, _, err := mime.ParseMediaType(meta)
	if err != nil {
		return brtypes.ImageBlock{}, core.NewInvalidRequestError("bedrock chat image data URL is missing a media type", err)
	}
	var format brtypes.ImageFormat
	switch mediaType {
	case "image/png":
		format = brtypes.ImageFormatPng
	case "image/jpeg", "image/jpg":
		format = brtypes.ImageFormatJpeg
	case "image/gif":
		format = brtypes.ImageFormatGif
	case "image/webp":
		format = brtypes.ImageFormatWebp
	default:
		return brtypes.ImageBlock{}, core.NewInvalidRequestError("bedrock chat image media type is not supported: "+mediaType, nil)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return brtypes.ImageBlock{}, core.NewInvalidRequestError("bedrock chat image data URL is not valid base64", err)
	}
	return brtypes.ImageBlock{Format: format, Source: &brtypes.ImageSourceMemberBytes{Value: raw}}, nil
}
