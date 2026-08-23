package core

import (
	"strconv"
	"strings"
)

// ImageEditRequest is an OpenAI-compatible POST /v1/images/edits request. The
// upstream call is multipart/form-data, so the image bytes and form fields are
// transport data rather than a JSON body. Only the fields the gateway needs to
// route and validate are typed; every other form field (n, size, quality,
// response_format, background, output_format, input_fidelity, user, ...) is
// preserved in Fields and forwarded upstream verbatim so new provider
// parameters work without a gateway change.
type ImageEditRequest struct {
	Model  string
	Prompt string
	// Images holds the source image(s) to edit. DALL·E 2 accepts exactly one;
	// gpt-image-1 accepts several (sent upstream as image[]).
	Images []ImageFile
	// Mask optionally marks the area to edit (transparent pixels are replaced).
	Mask *ImageFile
	// Fields carries the remaining form fields in request order, repeated
	// names included.
	Fields []FormField

	// Provider is gateway routing metadata, stripped before dispatching upstream.
	Provider string
}

// ImageFile is one uploaded image part of a multipart image request.
type ImageFile struct {
	Filename    string
	ContentType string
	Data        []byte
}

// FormField is a single multipart form value forwarded upstream unchanged.
type FormField struct {
	Name  string
	Value string
}

// Field returns the first value of the named form field and whether it was set.
func (r *ImageEditRequest) Field(name string) (string, bool) {
	if r == nil {
		return "", false
	}
	for _, f := range r.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// ValidateImageEditRequest enforces the fields every image edit provider
// requires before the request is routed.
func ValidateImageEditRequest(req *ImageEditRequest) error {
	if req == nil {
		return NewInvalidRequestError("image edit request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		return NewInvalidRequestError("model is required", nil)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return NewInvalidRequestError("prompt is required", nil)
	}
	if len(req.Images) == 0 {
		return NewInvalidRequestError("image is required", nil)
	}
	for _, img := range req.Images {
		if len(img.Data) == 0 {
			return NewInvalidRequestError("image file is empty", nil)
		}
	}
	if req.Mask != nil && len(req.Mask.Data) == 0 {
		return NewInvalidRequestError("mask file is empty", nil)
	}
	if n, ok := req.Field("n"); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(n)); err != nil || v < 1 {
			return NewInvalidRequestError("n must be at least 1", nil)
		}
	}
	// Streaming edits return server-sent events, which this endpoint does not relay.
	if stream, ok := req.Field("stream"); ok {
		if v, err := strconv.ParseBool(strings.TrimSpace(stream)); err != nil || v {
			return NewInvalidRequestError("streaming image edits are not supported; omit stream or set it to false", nil)
		}
	}
	return nil
}
