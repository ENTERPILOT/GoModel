package core

import "strings"

// Gemini API dialect (generativelanguage.googleapis.com). Its inference routes
// name the model and the method in the path instead of the body:
// /v1beta/models/{model}:{method}.
const (
	// GeminiAPIPathPrefix is the root of every Gemini-dialect route.
	GeminiAPIPathPrefix = "/v1beta/"

	GeminiMethodGenerateContent       = "generateContent"
	GeminiMethodStreamGenerateContent = "streamGenerateContent"

	// The endpoints usage and the request log record these routes under. The
	// request path carries the model, so recording it verbatim would split
	// one endpoint into one per model, which no operation filter could match;
	// the path template keeps it a single, filterable value.
	GeminiGenerateContentEndpoint       = geminiModelsPathPrefix + "{model}:" + GeminiMethodGenerateContent
	GeminiStreamGenerateContentEndpoint = geminiModelsPathPrefix + "{model}:" + GeminiMethodStreamGenerateContent

	geminiModelsPathPrefix = "/v1beta/models/"
)

// IsGeminiAPIPath reports whether path belongs to the Gemini-dialect surface.
func IsGeminiAPIPath(path string) bool {
	return strings.HasPrefix(path, GeminiAPIPathPrefix)
}

// ParseGeminiModelMethodPath splits /v1beta/models/{model}:{method} into its
// model and method. The model is everything up to the last colon, so
// provider-qualified IDs (openai/gpt-4o) and IDs that contain colons
// themselves (ollama/llama3:8b) both parse.
func ParseGeminiModelMethodPath(path string) (model, method string, ok bool) {
	rest, found := strings.CutPrefix(path, geminiModelsPathPrefix)
	if !found {
		return "", "", false
	}
	sep := strings.LastIndexByte(rest, ':')
	if sep <= 0 || sep == len(rest)-1 {
		return "", "", false
	}
	return rest[:sep], rest[sep+1:], true
}

// isGeminiInferencePath reports whether path is a Gemini-dialect route the
// gateway translates to chat.
func isGeminiInferencePath(path string) bool {
	_, method, ok := ParseGeminiModelMethodPath(path)
	return ok && (method == GeminiMethodGenerateContent || method == GeminiMethodStreamGenerateContent)
}

// RecordedEndpoint returns the endpoint a request path is recorded under in
// usage data and the request log: the path itself, except for routes that
// carry the model in the path, which are recorded under their path template.
func RecordedEndpoint(path string) string {
	if !IsGeminiAPIPath(path) {
		return path
	}
	_, method, ok := ParseGeminiModelMethodPath(path)
	if !ok {
		return path
	}
	switch method {
	case GeminiMethodGenerateContent:
		return GeminiGenerateContentEndpoint
	case GeminiMethodStreamGenerateContent:
		return GeminiStreamGenerateContentEndpoint
	}
	return path
}
