package auditlog

import (
	"bytes"
	"encoding/base64"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mediastore"
)

// imageMetaMaxBytes bounds the total string bytes kept in an image body's
// meta. The edit prompt and forwarded fields are client-controlled (up to the
// request body limit), so without a cap they could push the audit document
// past a store's per-record ceiling. Values beyond the cap are truncated and
// flagged.
const imageMetaMaxBytes = 1024 * 1024

// imageRevisedPromptMaxBytes bounds the provider-returned revised_prompt kept
// per image item; real revised prompts are a few hundred bytes.
const imageRevisedPromptMaxBytes = 16 * 1024

// ImageBodyLog is the audit representation of an image request or response.
// The "__images__" marker lets the dashboard detect it and render a gallery
// (for items with a MediaID) or labeled placeholders. Meta holds the
// surrounding parameters: prompt and options for an upload, the response
// envelope (created, usage, size, ...) for an output.
type ImageBodyLog struct {
	Images bool           `json:"__images__" bson:"__images__"`
	Items  []ImageItemLog `json:"images" bson:"images"`
	Meta   map[string]any `json:"meta,omitempty" bson:"meta,omitempty"`
}

// ImageItemLog is one image inside an ImageBodyLog. Role is "input" (an edit
// source), "mask", or "output". URL items (hosted DALL·E results) carry no
// bytes; stored items name their media object. Rows written before ADR-0013
// carry the pixels inline as base64 under "encoding" and "data" instead.
type ImageItemLog struct {
	Role          string `json:"role" bson:"role"`
	Filename      string `json:"filename,omitempty" bson:"filename,omitempty"`
	ContentType   string `json:"content_type,omitempty" bson:"content_type,omitempty"`
	Bytes         int64  `json:"bytes" bson:"bytes"`
	URL           string `json:"url,omitempty" bson:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty" bson:"revised_prompt,omitempty"`
	MediaID       string `json:"media_id,omitempty" bson:"media_id,omitempty"`
	Stored        bool   `json:"stored" bson:"stored"`
}

// BuildImageUploadBody builds the audit value for an image edit request: the
// uploaded source image(s) and optional mask plus the request parameters.
// Image bytes are stored through capture when it is non-nil; otherwise each
// item keeps its metadata.
func BuildImageUploadBody(capture *MediaCapture, images []core.ImageFile, mask *core.ImageFile, meta map[string]any) ImageBodyLog {
	body := ImageBodyLog{Images: true, Items: []ImageItemLog{}, Meta: capImageMeta(meta)}
	for _, img := range images {
		body.addUpload(capture, "input", img)
	}
	if mask != nil {
		body.addUpload(capture, "mask", *mask)
	}
	return body
}

// BuildImageResponseBody builds the audit value for an image generation or
// edit response. Hosted URLs are always kept; base64 images are stored
// through capture when it is non-nil, so a body logged without image storage
// stays small and complete (usage, size, quality) instead of being truncated
// mid-base64 by the generic capture limit.
func BuildImageResponseBody(capture *MediaCapture, resp *core.ImageGenerationResponse) ImageBodyLog {
	body := ImageBodyLog{Images: true, Items: []ImageItemLog{}}
	if resp == nil {
		return body
	}
	body.Meta = capImageMeta(imageResponseMeta(resp))
	contentType := imageOutputContentType(resp.OutputFormat)
	for _, data := range resp.Data {
		item := ImageItemLog{Role: "output", RevisedPrompt: truncateUTF8(data.RevisedPrompt, imageRevisedPromptMaxBytes)}
		switch {
		case data.URL != "":
			item.URL = data.URL
		case data.B64JSON != "":
			item.ContentType = contentType
			item.Bytes = int64(base64DecodedLen(data.B64JSON))
			if item.Bytes > 0 {
				item.attach(capture.save(mediastore.KindImage, contentType,
					base64.NewDecoder(base64.StdEncoding, strings.NewReader(data.B64JSON))))
			}
		}
		body.Items = append(body.Items, item)
	}
	return body
}

func (b *ImageBodyLog) addUpload(capture *MediaCapture, role string, img core.ImageFile) {
	item := ImageItemLog{
		Role:        role,
		Filename:    img.Filename,
		ContentType: bareMediaType(img.ContentType),
		Bytes:       int64(len(img.Data)),
	}
	if len(img.Data) > 0 {
		item.attach(capture.save(mediastore.KindImage, item.ContentType, bytes.NewReader(img.Data)))
	}
	b.Items = append(b.Items, item)
}

func (i *ImageItemLog) attach(object *mediastore.Object) {
	if object == nil {
		return
	}
	i.MediaID = object.ID
	i.Stored = true
	i.Bytes = object.Bytes
}

// capImageMeta bounds the total string bytes in an image body's meta so
// client-supplied parameters (a multi-megabyte prompt among them) cannot push
// the audit document past a store's per-record ceiling. Keys are visited in
// sorted order so truncation is deterministic; when anything is cut the meta
// is flagged with meta_truncated.
func capImageMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	names := make([]string, 0, len(meta))
	for name := range meta {
		names = append(names, name)
	}
	sort.Strings(names)
	remaining := imageMetaMaxBytes
	truncated := false
	capString := func(value string) string {
		if len(value) <= remaining {
			remaining -= len(value)
			return value
		}
		value = truncateUTF8(value, remaining)
		remaining = 0
		truncated = true
		return value
	}
	for _, name := range names {
		switch value := meta[name].(type) {
		case string:
			meta[name] = capString(value)
		case []string:
			for i, element := range value {
				value[i] = capString(element)
			}
		}
	}
	if truncated {
		meta["meta_truncated"] = true
	}
	return meta
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// imageResponseMeta copies the response envelope without the image payloads.
func imageResponseMeta(resp *core.ImageGenerationResponse) map[string]any {
	meta := map[string]any{"created": resp.Created}
	for key, value := range map[string]string{
		"background":    resp.Background,
		"output_format": resp.OutputFormat,
		"quality":       resp.Quality,
		"size":          resp.Size,
		"provider":      resp.Provider,
	} {
		if value != "" {
			meta[key] = value
		}
	}
	if resp.Usage != nil {
		meta["usage"] = imageUsageMeta(resp.Usage)
	}
	return meta
}

// imageUsageMeta flattens the usage block into plain maps so it serializes
// identically to JSON and BSON stores.
func imageUsageMeta(u *core.ImageUsage) map[string]any {
	usage := map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"total_tokens":  u.TotalTokens,
	}
	if d := u.InputTokensDetails; d != nil {
		usage["input_tokens_details"] = map[string]any{
			"text_tokens":  d.TextTokens,
			"image_tokens": d.ImageTokens,
		}
	}
	return usage
}

// imageOutputContentType maps an output_format (png, jpeg, webp) to a media
// type, defaulting to PNG, which every OpenAI image model emits by default.
func imageOutputContentType(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// bareMediaType strips MIME parameters so the stored type works in a
// Content-Type header.
func bareMediaType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// base64DecodedLen returns the byte length a base64 string decodes to, without
// decoding it. Malformed input (e.g. padding only) yields 0, never a negative
// length.
func base64DecodedLen(b64 string) int {
	n := len(b64)
	if n == 0 {
		return 0
	}
	padding := 0
	for i := n - 1; i >= 0 && i >= n-2 && b64[i] == '='; i-- {
		padding++
	}
	return max(n*3/4-padding, 0)
}
