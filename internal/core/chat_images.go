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

// ChatImageEntries returns the raw entries of a generated images member that
// are image_url parts with a URL, for producers that forward them as JSON.
// It is lenient: a malformed entry costs only itself, so readers that decode
// the whole member later still get every valid image, and a member that is
// not a list yields none.
func ChatImageEntries(raw []byte) []json.RawMessage {
	entries := chatImageList(raw)
	kept := entries[:0]
	for _, entry := range entries {
		if _, ok := decodeChatImage(entry); ok {
			kept = append(kept, entry)
		}
	}
	return kept
}

// ChatImageParts decodes the valid entries of a generated images member with
// the same leniency as ChatImageEntries. Each raw entry is released once
// decoded, so a multi-MB image is not held twice.
func ChatImageParts(raw []byte) []ContentPart {
	entries := chatImageList(raw)
	var images []ContentPart
	for i, entry := range entries {
		entries[i] = nil
		if image, ok := decodeChatImage(entry); ok {
			images = append(images, image)
		}
	}
	return images
}

func chatImageList(raw []byte) []json.RawMessage {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	return entries
}

func decodeChatImage(entry json.RawMessage) (ContentPart, bool) {
	var image ContentPart
	if json.Unmarshal(entry, &image) != nil || image.Type != "image_url" || image.ImageURL == nil || image.ImageURL.URL == "" {
		return ContentPart{}, false
	}
	return image, true
}
