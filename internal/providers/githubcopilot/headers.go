package githubcopilot

import (
	"context"
	"maps"
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Copilot request headers, as Copilot's own clients send them.
const (
	// initiatorHeader says who started the turn: "user" for a user message,
	// "agent" for a tool-call continuation.
	initiatorHeader = "X-Initiator"
	// visionHeader marks a request carrying images, as Copilot's clients
	// mark them.
	visionHeader = "Copilot-Vision-Request"
	intentHeader = "Openai-Intent"
	intent       = "conversation-edits"
)

// setHeaders applies the credential and the headers every Copilot request
// carries.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
	req.Header.Set(intentHeader, intent)
}

// chatHeaders derives the initiator and vision headers from a chat request.
func chatHeaders(_ context.Context, req *core.ChatRequest) http.Header {
	if req == nil || len(req.Messages) == 0 {
		return nil
	}
	vision := false
	for _, msg := range req.Messages {
		content, err := core.NormalizeMessageContent(msg.Content)
		if err != nil {
			continue
		}
		parts, _ := content.([]core.ContentPart)
		for _, part := range parts {
			if part.Type == "image_url" {
				vision = true
			}
		}
	}
	return requestHeaders(req.Messages[len(req.Messages)-1].Role == "user", vision)
}

// addResponsesHeaders adds the initiator and vision headers to a request for
// the native /responses endpoint. Chat requests get theirs from chatHeaders.
func addResponsesHeaders(req *llmclient.Request) {
	body, ok := req.Body.(*core.ResponsesRequest)
	if !ok || body == nil {
		return
	}
	headers := responsesHeaders(body.Input)
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	maps.Copy(req.Headers, headers)
}

// responsesHeaders inspects Responses input, which arrives in several Go
// shapes (a string, decoded JSON, or typed elements), through its JSON form.
func responsesHeaders(input any) http.Header {
	if _, ok := input.(string); ok {
		return requestHeaders(true, false)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return requestHeaders(true, false)
	}
	// A message carries its parts in content; a function_call_output, such
	// as a tool result with an image, carries them in output.
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Output  json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return requestHeaders(true, false)
	}
	vision := false
	for _, item := range items {
		vision = vision || hasInputImage(item.Content) || hasInputImage(item.Output)
	}
	return requestHeaders(items[len(items)-1].Role == "user", vision)
}

// hasInputImage reports whether raw is a list of content parts with an image.
func hasInputImage(raw json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if part.Type == "input_image" {
			return true
		}
	}
	return false
}

func requestHeaders(userTurn, vision bool) http.Header {
	initiator := "agent"
	if userTurn {
		initiator = "user"
	}
	headers := http.Header{}
	headers.Set(initiatorHeader, initiator)
	if vision {
		headers.Set(visionHeader, "true")
	}
	return headers
}
