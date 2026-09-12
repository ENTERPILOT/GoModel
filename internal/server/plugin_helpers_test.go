package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
	"github.com/enterpilot/gomodel/pluginapi"
)

// memoryGuardrailStore is an in-memory guardrails.Store for handler tests.
type memoryGuardrailStore struct {
	definitions map[string]guardrails.Definition
}

func (s *memoryGuardrailStore) List(context.Context) ([]guardrails.Definition, error) {
	out := make([]guardrails.Definition, 0, len(s.definitions))
	for _, def := range s.definitions {
		out = append(out, def)
	}
	return out, nil
}

func (s *memoryGuardrailStore) Get(_ context.Context, name string) (*guardrails.Definition, error) {
	def, ok := s.definitions[name]
	if !ok {
		return nil, guardrails.ErrNotFound
	}
	return &def, nil
}

func (s *memoryGuardrailStore) Upsert(_ context.Context, def guardrails.Definition) error {
	s.definitions[def.Name] = def
	return nil
}

func (s *memoryGuardrailStore) UpsertMany(_ context.Context, defs []guardrails.Definition) error {
	for _, def := range defs {
		s.definitions[def.Name] = def
	}
	return nil
}

func (s *memoryGuardrailStore) Delete(_ context.Context, name string) error {
	delete(s.definitions, name)
	return nil
}

func (s *memoryGuardrailStore) Close() error { return nil }

// newGuardrailChains builds chains from definitions over the built-in
// plugins plus any extra plugin factories.
func newGuardrailChains(t *testing.T, chat plugins.ChatCompleter, steps []guardrails.StepReference, extra []func() pluginapi.Plugin, definitions ...guardrails.Definition) *plugins.Chains {
	t.Helper()
	catalog := plugins.NewCatalog()
	for _, factory := range append(builtin.All(), extra...) {
		err := catalog.Register(factory, plugins.SourceBuiltin)
		require.NoError(t, err)
	}
	store := &memoryGuardrailStore{definitions: map[string]guardrails.Definition{}}
	for _, def := range definitions {
		store.definitions[def.Name] = def
	}
	service, err := guardrails.NewService(store, catalog, plugins.HostDeps{Chat: chat})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	chains, err := service.BuildChains(steps)
	require.NoError(t, err)

	return chains
}

// newSystemPromptChains builds a prompt chain with one inject-mode
// system_prompt guardrail named "test".
func newSystemPromptChains(t *testing.T, content string) *plugins.Chains {
	t.Helper()
	config, _ := json.Marshal(map[string]string{"mode": "inject", "content": content})
	return newGuardrailChains(t, nil, []guardrails.StepReference{{Ref: "test", Step: 0}}, nil,
		guardrails.Definition{Name: "test", Type: "system_prompt", Config: config})
}
