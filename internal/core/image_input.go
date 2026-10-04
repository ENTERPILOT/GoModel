package core

import (
	"bytes"
	"context"
	"sync"

	"github.com/tidwall/gjson"
)

// imageInputProbeKey stores the lazy image-input check for the request.
const imageInputProbeKey contextKey = "image-input-probe"

// WithImageInputProbe attaches a lazy check for whether the request carries
// image input. The probe runs at most once, and only when routing asks: most
// requests never pay for reading the body.
func WithImageInputProbe(ctx context.Context, probe func() bool) context.Context {
	if probe == nil {
		return ctx
	}
	return context.WithValue(ctx, imageInputProbeKey, sync.OnceValue(probe))
}

// RequestHasImageInput reports whether the request carries image input. It is
// false when no probe is attached.
func RequestHasImageInput(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	probe, ok := ctx.Value(imageInputProbeKey).(func() bool)
	return ok && probe()
}

// imagePartTypes are the content-part types that carry an image in the
// request dialects the gateway accepts: Chat Completions (image_url),
// Responses (input_image), and Anthropic Messages (image).
var imagePartTypes = map[string]struct{}{
	"image_url":   {},
	"input_image": {},
	"image":       {},
}

// maxImagePartDepth bounds the walk below messages/input: message, content
// list, part, and one nested level for tool results that return images.
const maxImagePartDepth = 6

// BodyHasImageInput reports whether a Chat Completions, Responses, or
// Anthropic Messages JSON body carries an image content part. It scans the
// raw bytes without decoding the request.
func BodyHasImageInput(body []byte) bool {
	if !bytes.Contains(body, []byte(`image`)) {
		return false
	}
	root := gjson.ParseBytes(body)
	return hasImagePart(root.Get("messages"), 0) || hasImagePart(root.Get("input"), 0)
}

// hasImagePart walks message and content lists looking for an image part.
// Nested content and output lists cover Anthropic tool_result blocks and
// Responses tool outputs that return images.
func hasImagePart(value gjson.Result, depth int) bool {
	if depth > maxImagePartDepth {
		return false
	}
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool {
			found = hasImagePart(item, depth+1)
			return !found
		})
		return found
	}
	if !value.IsObject() {
		return false
	}
	if _, ok := imagePartTypes[value.Get("type").String()]; ok {
		return true
	}
	return hasImagePart(value.Get("content"), depth+1) || hasImagePart(value.Get("output"), depth+1)
}
