package core

import (
	"fmt"
	"slices"
	"strings"
)

// ChatUnsupportedReason explains why a model with this catalog metadata
// cannot answer a chat completion, or returns "" when it can or the metadata
// does not say. A model that lists generation modes must list chat,
// responses, or messages (the gateway answers chat through Responses or
// Messages for a model that serves only those). A utility model with no mode is a decision model, such as Jev or
// Kev, which answers decision requests only.
func ChatUnsupportedReason(meta *ModelMetadata) string {
	if meta == nil {
		return ""
	}
	if len(meta.Modes) > 0 {
		if slices.ContainsFunc(meta.Modes, func(mode string) bool { return mode == "chat" || mode == "responses" || mode == "messages" }) {
			return ""
		}
		return fmt.Sprintf("serves %s only, not chat", strings.Join(meta.Modes, "/"))
	}
	if slices.Contains(meta.Categories, CategoryUtility) {
		return "is a decision model (it answers /v1/decisions or /v1/systemone), not a chat model"
	}
	return ""
}
