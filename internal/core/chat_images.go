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
