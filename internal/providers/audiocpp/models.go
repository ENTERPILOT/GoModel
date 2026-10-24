package audiocpp

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// modelsResponse mirrors audiocpp_server's /v1/models payload. It restates the
// OpenAI-compatible fields core.Model already carries because the entries also
// describe the configured model itself — which family implements it, which
// task it was registered for, and whether it runs offline or streaming — and
// the plain OpenAI shape has nowhere to put those.
type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Family  string `json:"family"`
	Task    string `json:"task"`
	Mode    string `json:"mode"`
}

// ListModels returns the models audiocpp_server was configured with. The
// gateway can only route the two tasks it has OpenAI endpoints for, so only
// those carry a mode; the rest stay listed without one, since they are still
// reachable through native passthrough.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var raw modelsResponse
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/models",
	}, &raw); err != nil {
		return nil, err
	}
	return raw.toCore(), nil
}

func (r *modelsResponse) toCore() *core.ModelsResponse {
	object := strings.TrimSpace(r.Object)
	if object == "" {
		object = "list"
	}
	resp := &core.ModelsResponse{Object: object, Data: make([]core.Model, 0, len(r.Data))}
	for _, entry := range r.Data {
		resp.Data = append(resp.Data, entry.toCore())
	}
	return resp
}

func (e modelEntry) toCore() core.Model {
	object := strings.TrimSpace(e.Object)
	if object == "" {
		object = "model"
	}
	metadata := core.ModelMetadata{
		Family:     strings.TrimSpace(e.Family),
		Modes:      modesForTask(e.Task),
		Categories: []core.ModelCategory{core.CategoryAudio},
	}
	// A streaming model is the one a client may ask for transcript or speech
	// events; an offline one answers a streamed request with a single reply.
	if strings.EqualFold(strings.TrimSpace(e.Mode), "streaming") {
		metadata.Capabilities = map[string]bool{"streaming": true}
	}
	return core.Model{
		ID:       strings.TrimSpace(e.ID),
		Object:   object,
		OwnedBy:  strings.TrimSpace(e.OwnedBy),
		Metadata: &metadata,
	}
}

// modesForTask maps an audio.cpp task onto the gateway's mode vocabulary.
// audio.cpp registers many more tasks than the two OpenAI audio endpoints
// cover (alignment, diarization, separation, VAD, ...); those get no mode
// rather than a made-up one, so nothing advertises them as speech or
// transcription models the router could send an OpenAI request to.
func modesForTask(task string) []string {
	switch strings.ToLower(strings.TrimSpace(task)) {
	case "tts":
		return []string{"audio_speech"}
	case "asr":
		return []string{"audio_transcription"}
	default:
		return nil
	}
}
