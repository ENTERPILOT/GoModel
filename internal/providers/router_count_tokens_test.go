package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

type mockTokenCountingProvider struct {
	*mockProvider
	count     int
	lastModel string
	lastBody  []byte
}

func (m *mockTokenCountingProvider) CountMessagesTokens(_ context.Context, model string, body []byte) (int, error) {
	m.lastModel, m.lastBody = model, body
	if m.err != nil {
		return 0, m.err
	}
	return m.count, nil
}

// The router hands the count to the provider that owns the model, with the
// provider prefix stripped, and reports plainly when that provider has no
// count endpoint so the caller can estimate instead.
func TestRouterCountMessagesTokens(t *testing.T) {
	counter := &mockTokenCountingProvider{mockProvider: &mockProvider{name: "anthropic"}, count: 321}
	plain := &mockProvider{name: "openai"}
	lookup := newMockLookup()
	lookup.addModel("anthropic/claude-haiku-4-5", counter, "anthropic")
	lookup.addModel("openai/gpt-5-mini", plain, "openai")
	router, _ := NewRouter(lookup)

	got, err := router.CountMessagesTokens(context.Background(), "anthropic/claude-haiku-4-5", []byte(`{"messages":[]}`))
	if err != nil {
		t.Fatalf("CountMessagesTokens: %v", err)
	}
	if got != 321 || counter.lastModel != "claude-haiku-4-5" {
		t.Errorf("count = %d via model %q, want 321 via the bare model", got, counter.lastModel)
	}

	_, err = router.CountMessagesTokens(context.Background(), "openai/gpt-5-mini", []byte(`{"messages":[]}`))
	if !errors.Is(err, core.ErrMessagesTokenCountUnsupported) {
		t.Errorf("err = %v, want ErrMessagesTokenCountUnsupported for a provider without the endpoint", err)
	}
}
