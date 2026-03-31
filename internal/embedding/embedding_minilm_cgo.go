//go:build cgo

package embedding

import (
	"context"
	"fmt"
	"os"

	all_minilm "github.com/clems4ever/all-minilm-l6-v2-go/all_minilm_l6_v2"
)

// MiniLMEmbedder uses the local all-MiniLM-L6-v2 ONNX model.
// No network calls are made; the model runs in-process.
type miniLMEmbedder struct {
	model *all_minilm.Model
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
	type result struct {
		vec []float32
		err error
	}
	ch := make(chan result, 1)
	go func() {
		vec, err := e.model.Compute(text, true)
		ch <- result{vec, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("embedding: MiniLM compute failed: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("embedding: MiniLM compute failed: %w", r.err)
		}
		return r.vec, nil
	}
}

func (e *miniLMEmbedder) Close() error {
	if e.model != nil {
		e.model.Close()
	}
	return nil
}
