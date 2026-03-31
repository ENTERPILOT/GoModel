//go:build !cgo

package embedding

import (
	"context"
	"fmt"
)

func newMiniLMEmbedder(string) (*miniLMEmbedder, error) {
	return nil, fmt.Errorf("embedding: local MiniLM embedder requires cgo-enabled builds")
}

type miniLMEmbedder struct{}

func (*miniLMEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("embedding: local MiniLM embedder requires cgo-enabled builds")
}

func (*miniLMEmbedder) Close() error { return nil }
