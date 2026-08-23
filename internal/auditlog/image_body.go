package auditlog

import (
	"encoding/base64"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// imageBodyMaxBytes caps the total *raw* image bytes embedded as base64 in one
// audit body. A multi-image response (n=4 at 1024x1024) can easily exceed a
// document store's per-record ceiling (MongoDB: 16 MB BSON), so images are
// stored in order until the budget is spent and the rest are recorded as
// metadata-only placeholders.
const imageBodyMaxBytes = 8 * 1024 * 1024

// ImageBodyLog is the audit representation of an image request or response.
// The "__images__" marker lets the dashboard detect it and render a gallery
// (when items carry Data) or labeled placeholders. Meta holds the surrounding
// parameters: prompt and options for an upload, the response envelope
// (created, usage, size, ...) for an output.
type ImageBodyLog struct {
	Images bool           `json:"__images__" bson:"__images__"`
	Items  []ImageItemLog `json:"images" bson:"images"`
	Meta   map[string]any `json:"meta,omitempty" bson:"meta,omitempty"`

	storedBytes int
}

// ImageItemLog is one image inside an ImageBodyLog. Role is "input" (an edit
// source), "mask", or "output". URL items (hosted DALL·E results) carry no
// bytes; base64 items carry Data when Stored is true.
type ImageItemLog struct {
	Role          string `json:"role" bson:"role"`
	Filename      string `json:"filename,omitempty" bson:"filename,omitempty"`
	ContentType   string `json:"content_type,omitempty" bson:"content_type,omitempty"`
	Bytes         int    `json:"bytes" bson:"bytes"`
	URL           string `json:"url,omitempty" bson:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty" bson:"revised_prompt,omitempty"`
	Encoding      string `json:"encoding,omitempty" bson:"encoding,omitempty"`
	Data          string `json:"data,omitempty" bson:"data,omitempty"`
	Stored        bool   `json:"stored" bson:"stored"`
	TooLarge      bool   `json:"too_large,omitempty" bson:"too_large,omitempty"`
}

// BuildImageUploadBody builds the audit value for an image edit request: the
// uploaded source image(s) and optional mask plus the request parameters.
// Image bytes are embedded (base64) only when storeBytes is true and the
// entry's image budget allows; otherwise each item keeps its metadata.
func BuildImageUploadBody(images []core.ImageFile, mask *core.ImageFile, storeBytes bool, meta map[string]any) ImageBodyLog {
	body := ImageBodyLog{Images: true, Items: []ImageItemLog{}, Meta: meta}
	for _, img := range images {
		body.addRaw("input", img, storeBytes)
	}
	if mask != nil {
		body.addRaw("mask", *mask, storeBytes)
	}
	return body
}

// BuildImageResponseBody builds the audit value for an image generation or
// edit response. Hosted URLs are always kept; base64 images are embedded only
// when storeBytes is true and within budget, so a body logged without image
// storage stays small and complete (usage, size, quality) instead of being
// truncated mid-base64 by the generic capture limit.
func BuildImageResponseBody(resp *core.ImageGenerationResponse, storeBytes bool) ImageBodyLog {
	body := ImageBodyLog{Images: true, Items: []ImageItemLog{}}
	if resp == nil {
		return body
	}
	body.Meta = imageResponseMeta(resp)
	contentType := imageOutputContentType(resp.OutputFormat)
	for _, data := range resp.Data {
		item := ImageItemLog{Role: "output", RevisedPrompt: data.RevisedPrompt}
		switch {
		case data.URL != "":
			item.URL = data.URL
		case data.B64JSON != "":
			item.ContentType = contentType
			item.Bytes = base64DecodedLen(data.B64JSON)
			body.store(&item, data.B64JSON, storeBytes)
		}
		body.Items = append(body.Items, item)
	}
	return body
}

func (b *ImageBodyLog) addRaw(role string, img core.ImageFile, storeBytes bool) {
	item := ImageItemLog{
		Role:        role,
		Filename:    img.Filename,
		ContentType: bareMediaType(img.ContentType),
		Bytes:       len(img.Data),
	}
	if len(img.Data) > 0 {
		b.store(&item, base64.StdEncoding.EncodeToString(img.Data), storeBytes)
	}
	b.Items = append(b.Items, item)
}

// store attaches the base64 payload when storage is enabled and the item fits
// the remaining budget; otherwise it flags the item as too large.
func (b *ImageBodyLog) store(item *ImageItemLog, b64 string, storeBytes bool) {
	if !storeBytes || item.Bytes == 0 {
		return
	}
	if b.storedBytes+item.Bytes > imageBodyMaxBytes {
		item.TooLarge = true
		return
	}
	b.storedBytes += item.Bytes
	item.Encoding = "base64"
	item.Data = b64
	item.Stored = true
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

// bareMediaType strips MIME parameters so the stored type works in a data: URL.
func bareMediaType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// base64DecodedLen returns the byte length a base64 string decodes to, without
// decoding it.
func base64DecodedLen(b64 string) int {
	n := len(b64)
	if n == 0 {
		return 0
	}
	padding := 0
	for i := n - 1; i >= 0 && i >= n-2 && b64[i] == '='; i-- {
		padding++
	}
	return n*3/4 - padding
}
