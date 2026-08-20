package llamacpp

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// modelsResponse mirrors llama-server's /v1/models payload. It restates the
// OpenAI-compatible fields core.Model already carries because the response also
// holds a "meta" object describing the loaded GGUF, which the plain OpenAI
// shape drops — decoding it here keeps the llama.cpp-specific field in this
// package instead of in core.
type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

type modelEntry struct {
	ID      string     `json:"id"`
	Object  string     `json:"object"`
	OwnedBy string     `json:"owned_by"`
	Created int64      `json:"created"`
	Meta    *modelMeta `json:"meta"`
}

// modelMeta is llama-server's description of the GGUF behind a model entry.
// n_ctx is the context the server is running with and n_ctx_train the one the
// model was trained for; the rest of the object (n_embd, n_params, size,
// n_vocab, vocab_type, ftype) stays undecoded rather than leaking llama.cpp
// internals into the OpenAI-compatible model shape.
type modelMeta struct {
	NCtx      int `json:"n_ctx"`
	NCtxTrain int `json:"n_ctx_train"`
}

// serverProps is the subset of llama-server's /props response we surface.
// default_generation_settings.n_ctx is the per-slot context the server was
// actually started with — the limit a request is measured against.
type serverProps struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	Modalities map[string]bool `json:"modalities"`
}

// ListModels retrieves the list of available models from llama-server, keeping
// the context window and modalities the server reports for the model it has
// loaded so clients can size requests without a model-registry entry.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var raw modelsResponse
	if err := p.compatible.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &raw); err != nil {
		return nil, err
	}
	return raw.toCore(p.fetchServerProps(ctx, len(raw.Data))), nil
}

// fetchServerProps reads the running server's own state, or nil when it is
// unavailable or cannot be attributed. /props describes the single model
// llama-server has loaded, so it is only meaningful for a single-entry listing;
// llama.cpp's router mode serves several models from one process, and each of
// those already carries its own meta.n_ctx. Failures are ignored — /props is
// enrichment, and servers that do not implement it (LM Studio answers 200 with
// an error body) must still list their models.
func (p *Provider) fetchServerProps(ctx context.Context, modelCount int) *serverProps {
	if modelCount != 1 {
		return nil
	}
	var props serverProps
	if err := p.rootClient.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/props",
	}, &props); err != nil {
		return nil
	}
	return &props
}

func (r *modelsResponse) toCore(props *serverProps) *core.ModelsResponse {
	object := strings.TrimSpace(r.Object)
	if object == "" {
		object = "list"
	}
	resp := &core.ModelsResponse{Object: object, Data: make([]core.Model, 0, len(r.Data))}
	for _, entry := range r.Data {
		resp.Data = append(resp.Data, entry.toCore(props))
	}
	return resp
}

func (e modelEntry) toCore(props *serverProps) core.Model {
	object := strings.TrimSpace(e.Object)
	if object == "" {
		object = "model"
	}
	model := core.Model{
		ID:      strings.TrimSpace(e.ID),
		Object:  object,
		OwnedBy: strings.TrimSpace(e.OwnedBy),
		Created: e.Created,
	}

	// Modes stay unset on purpose. llama-server reports nothing that separates a
	// chat model from an embedding one, so leaving them empty keeps the
	// registry's ID heuristic free to classify local GGUFs.
	metadata := core.ModelMetadata{}
	if contextWindow := e.contextWindow(props); contextWindow > 0 {
		metadata.ContextWindow = &contextWindow
	}
	if props != nil {
		metadata.Capabilities = modalityCapabilities(props.Modalities)
	}
	if metadata.ContextWindow == nil && len(metadata.Capabilities) == 0 {
		// Nothing was reported; leaving Metadata nil keeps the model catalog and
		// the ID heuristic free to supply everything.
		return model
	}
	model.Metadata = &metadata
	return model
}

// contextWindow resolves the limit a request is actually measured against,
// strongest source first:
//
//   - meta.n_ctx — the per-slot context the server is running with, reported
//     per model, so it stays correct when one process serves several.
//   - /props — the same number, for builds whose listing predates meta.n_ctx.
//   - meta.n_ctx_train — the GGUF's trained ceiling. Only an upper bound:
//     llama-server's --ctx-size default sits far below it for most models, so
//     it is a last resort rather than the headline figure.
func (e modelEntry) contextWindow(props *serverProps) int {
	if e.Meta != nil && e.Meta.NCtx > 0 {
		return e.Meta.NCtx
	}
	if props != nil && props.DefaultGenerationSettings.NCtx > 0 {
		return props.DefaultGenerationSettings.NCtx
	}
	if e.Meta != nil {
		return e.Meta.NCtxTrain
	}
	return 0
}

// modalityCapabilities maps llama-server's multimodal flags onto GoModel
// capability keys. Unsupported modalities are omitted rather than recorded as
// false, so a later metadata layer can still claim them.
func modalityCapabilities(modalities map[string]bool) map[string]bool {
	capabilities := make(map[string]bool, len(modalities))
	for modality, supported := range modalities {
		name := strings.ToLower(strings.TrimSpace(modality))
		if !supported || name == "" {
			continue
		}
		capabilities[name] = true
	}
	if len(capabilities) == 0 {
		return nil
	}
	return capabilities
}
