package core

import "github.com/goccy/go-json"

// ChatImagesField is the assistant message member that carries generated
// images, following OpenRouter's convention: message.content stays a string
// and images arrive as image_url parts with base64 data URLs.
const ChatImagesField = "images"

// ChatImages decodes the generated images on an assistant message.
func ChatImages(fields UnknownJSONFields) ([]ContentPart, error) {
	raw := fields.Lookup(ChatImagesField)
	if IsJSONNull(raw) {
		return nil, nil
	}
	var images []ContentPart
	if err := json.Unmarshal(raw, &images); err != nil {
		return nil, err
	}
	return images, nil
}

// ChatImageEntries decodes a generated images member leniently, for the
// producers that assemble or convert one: it returns the entries that are
// image_url parts with a URL, both raw and decoded, and skips the rest. A
// member that is not a list yields none. Unlike ChatImages, one malformed
// entry costs only itself, so readers that decode the whole member later
// still get every valid image.
func ChatImageEntries(raw []byte) ([]json.RawMessage, []ContentPart) {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil, nil
	}
	var kept []json.RawMessage
	var images []ContentPart
	for _, entry := range entries {
		var image ContentPart
		if json.Unmarshal(entry, &image) != nil || image.Type != "image_url" || image.ImageURL == nil || image.ImageURL.URL == "" {
			continue
		}
		kept = append(kept, entry)
		images = append(images, image)
	}
	return kept, images
}
