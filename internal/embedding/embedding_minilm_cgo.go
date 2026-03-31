//go:build cgo

package embedding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	all_minilm "github.com/clems4ever/all-minilm-l6-v2-go/all_minilm_l6_v2"

	"gomodel/internal/core"
)

// MiniLMEmbedder uses the local all-MiniLM-L6-v2 ONNX model.
// No network calls are made; the model runs in-process.
type miniLMEmbedder struct {
	model *all_minilm.Model
}

var miniLMModelCompute = func(model *all_minilm.Model, text string) ([]float32, error) {
	return model.Compute(text, true)
}

func newMiniLMEmbedder(runtimePath string) (*miniLMEmbedder, error) {
	if runtimePath == "" {
		runtimePath = os.Getenv("ONNXRUNTIME_LIB_PATH")
	}
	var opts []all_minilm.ModelOption
	if runtimePath != "" {
		opts = append(opts, all_minilm.WithRuntimePath(runtimePath))
	}
	m, err := all_minilm.NewModel(opts...)
	if err != nil {
		return nil, fmt.Errorf("embedding: failed to load local MiniLM model: %w", err)
	}
	return &miniLMEmbedder{model: m}, nil
}

func (e *miniLMEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, core.NewInvalidRequestError("embedding: MiniLM compute canceled", err)
	}

	vec, err := miniLMModelCompute(e.model, text)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, core.NewInvalidRequestError("embedding: MiniLM compute canceled", errors.Join(ctxErr, err))
		}
		return nil, core.NewProviderError("local", http.StatusBadGateway, "embedding: MiniLM compute failed", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, core.NewInvalidRequestError("embedding: MiniLM compute canceled", err)
	}
	return vec, nil
}

func (e *miniLMEmbedder) Close() error {
	if e.model != nil {
		e.model.Close()
	}
	return nil
}
