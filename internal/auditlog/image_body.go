package auditlog

import (
	"encoding/base64"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
)

// imageBodyMaxBytes caps the *stored base64* image bytes in one audit entry,
// across its request and response bodies together. The budget is charged in
// encoded bytes — the size the document store actually persists — so together
// with the capped meta (imageMetaMaxBytes), the capped middleware body
// captures (MaxBodyCapture), and headers, a full entry stays well under a
// document store's per-record ceiling (MongoDB: 16 MiB BSON). Images are
// stored in order until the entry's budget is spent and the rest are recorded
// as metadata-only placeholders.
const imageBodyMaxBytes = 8 * 1024 * 1024

// imageMetaMaxBytes bounds the total string bytes kept in an image body's
// meta. The edit prompt and forwarded fields are client-controlled (up to the
// request body limit), so without a cap they could consume the headroom the
// image budget leaves. Values beyond the cap are truncated and flagged.
const imageMetaMaxBytes = 1024 * 1024

// imageRevisedPromptMaxBytes bounds the provider-returned revised_prompt kept
// per image item; real revised prompts are a few hundred bytes.
const imageRevisedPromptMaxBytes = 16 * 1024

// ImageBodyBudget tracks one audit entry's remaining image-byte allowance.
// Share a single budget between the request and response image bodies of the
// same entry.
type ImageBodyBudget struct {
	remaining int
}

// NewImageBodyBudget returns a fresh per-entry budget.
func NewImageBodyBudget() *ImageBodyBudget {
	return &ImageBodyBudget{remaining: imageBodyMaxBytes}
}

// take reserves size encoded bytes, reporting whether they fit. Non-positive
// sizes are rejected so a caller bug can never grow the budget.
func (b *ImageBodyBudget) take(size int) bool {
	if size <= 0 || size > b.remaining {
		return false
	}
	b.remaining -= size
	return true
}

// ImageBodyLog is the audit representation of an image request or response.
// The "__images__" marker lets the dashboard detect it and render a gallery
// (when items carry Data) or labeled placeholders. Meta holds the surrounding
// parameters: prompt and options for an upload, the response envelope
// (created, usage, size, ...) for an output.
type ImageBodyLog struct {
	Images bool           `json:"__images__" bson:"__images__"`
	Items  []ImageItemLog `json:"images" bson:"images"`
	Meta   map[string]any `json:"meta,omitempty" bson:"meta,omitempty"`

	budget *ImageBodyBudget
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
// Image bytes are embedded (base64) only when storeBytes is true and budget —
// the entry-wide allowance, shared with the response body — allows; otherwise
// each item keeps its metadata. A nil budget starts a fresh one.
func BuildImageUploadBody(images []core.ImageFile, mask *core.ImageFile, storeBytes bool, meta map[string]any, budget *ImageBodyBudget) ImageBodyLog {
	body := ImageBodyLog{Images: true, Items: []ImageItemLog{}, Meta: capImageMeta(meta), budget: budget}
	if body.budget == nil {
		body.budget = NewImageBodyBudget()
	}
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
// truncated mid-base64 by the generic capture limit. budget is the entry-wide
// image allowance (shared with an edit's upload body); nil starts a fresh one.
func BuildImageResponseBody(resp *core.ImageGenerationResponse, storeBytes bool, budget *ImageBodyBudget) ImageBodyLog {
	body := ImageBodyLog{Images: true, Items: []ImageItemLog{}, budget: budget}
	if body.budget == nil {
		body.budget = NewImageBodyBudget()
	}
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
// the entry's remaining budget; otherwise it flags the item as too large. The
// budget is charged with the encoded length — what the audit store persists —
// not the smaller decoded size.
func (b *ImageBodyLog) store(item *ImageItemLog, b64 string, storeBytes bool) {
	if !storeBytes || item.Bytes == 0 {
		return
	}
	if !b.budget.take(len(b64)) {
		item.TooLarge = true
		return
	}
	item.Encoding = "base64"
	item.Data = b64
	item.Stored = true
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

// bareMediaType strips MIME parameters so the stored type works in a data: URL.
func bareMediaType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// base64DecodedLen returns the byte length a base64 string decodes to, without
// decoding it. Malformed input (e.g. padding only) yields 0, never a negative
// length that would corrupt the entry's image budget.
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
