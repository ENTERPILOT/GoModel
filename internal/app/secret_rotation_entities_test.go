package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEntitySecrets struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeEntitySecrets) RotateSecrets(_ context.Context, fields []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fields)
	return f.err
}

func recordEntityField(t *testing.T, h *rotationHarness, field, reference, value string) {
	t.Helper()
	h.vault.set(reference, value)
	stored := "${vault:" + reference + "}"
	require.NoError(t, h.rotation.secrets.ResolveFields(t.Context(), field, &stored))
}

func TestSecretRotationHandsEntityFieldsToTheirOwners(t *testing.T) {
	h := newRotationHarness(t, nil)
	credentials := &fakeEntitySecrets{}
	mcp := &fakeEntitySecrets{}
	h.rotation.watchEntities("provider_credentials.", credentials)
	h.rotation.watchEntities("mcp_servers.", mcp)
	recordEntityField(t, h, "provider_credentials.managed.api_keys[0]", "managed", "m1")
	recordEntityField(t, h, "mcp_servers.github.headers.Authorization", "gh", "g1")

	// Only an entity field changed: its owner reinstalls it, the
	// configuration is neither swapped nor reloaded.
	h.vault.set("managed", "m2")
	h.rotation.check(t.Context())
	assert.Equal(t, [][]string{{"provider_credentials.managed.api_keys[0]"}}, credentials.calls)
	assert.Empty(t, mcp.calls)
	assert.Zero(t, h.planned.Load())
	assert.Empty(t, h.reloads)

	// Entity and configuration fields together: each goes its own way. (The
	// fake owner does not re-record, so its field is reported again.)
	h.vault.set("gh", "g2")
	h.vault.set("dsn", "d2")
	h.rotation.check(t.Context())
	assert.Equal(t, [][]string{{"mcp_servers.github.headers.Authorization"}}, mcp.calls)
	require.Len(t, h.reloads, 1)
	reason := <-h.reloads
	assert.Contains(t, reason, "storage.postgresql.url")
	assert.NotContains(t, reason, "mcp_servers")
}

func TestSecretRotationEntityFailureDoesNotBlockTheConfiguration(t *testing.T) {
	h := newRotationHarness(t, &fakeKeySwap{providers: []string{"openai"}})
	guardrails := &fakeEntitySecrets{err: errors.New("instance did not build")}
	h.rotation.watchEntities("guardrail_definitions.", guardrails)
	recordEntityField(t, h, "guardrail_definitions.pii.config.api_key", "pii", "p1")

	h.vault.set("pii", "p2")
	h.vault.set("openai", "k2")
	h.rotation.check(t.Context())
	assert.Len(t, guardrails.calls, 1)
	assert.Equal(t, int32(1), h.swap.applied.Load())
}
