package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasSecretReference(t *testing.T) {
	assert.True(t, HasSecretReference("${vault:a}"))
	assert.True(t, HasSecretReference("Bearer ${env:TOKEN}"))
	assert.False(t, HasSecretReference("sk-literal"))
	assert.False(t, HasSecretReference("$${vault:a}"))
	assert.False(t, HasSecretReference("${LEGACY}"))
}

func TestResolveEntityRecordsOnlyOnRecord(t *testing.T) {
	vault := newFakeVault(map[string]string{"a": "one", "b": "two"})
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	ctx := t.Context()

	resolved, err := secrets.ResolveEntity(ctx, "mcp_servers.gh", map[string]string{
		"mcp_servers.gh.headers.A": "Bearer ${vault:a}",
		"mcp_servers.gh.headers.B": "literal",
	})
	require.NoError(t, err)
	assert.Equal(t, "Bearer one", resolved.Value("mcp_servers.gh.headers.A"))
	assert.Equal(t, "literal", resolved.Value("mcp_servers.gh.headers.B"))

	vault.set("a", "rotated")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields(), "nothing is watched before Record")

	resolved.Record()
	recheck, err = secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp_servers.gh.headers.A"}, recheck.Fields())
	value, ok := recheck.Value("mcp_servers.gh.headers.A")
	require.True(t, ok)
	assert.Equal(t, "Bearer rotated", value)
}

func TestResolvedEntityRecordReplacesEntityFields(t *testing.T) {
	vault := newFakeVault(map[string]string{"a": "one", "b": "two"})
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	ctx := t.Context()

	first, err := secrets.ResolveEntity(ctx, "e", map[string]string{"e.x": "${vault:a}", "e.y": "${vault:b}"})
	require.NoError(t, err)
	first.Record()
	other, err := secrets.ResolveEntity(ctx, "e.y", map[string]string{"e.y.z": "${vault:b}"})
	require.NoError(t, err)
	other.Record()

	// e.y is now a literal: only e.x stays watched for entity e.
	second, err := secrets.ResolveEntity(ctx, "e", map[string]string{"e.x": "${vault:a}", "e.y": "literal"})
	require.NoError(t, err)
	second.Record()

	vault.set("a", "a2")
	vault.set("b", "b2")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"e.x", "e.y.z"}, recheck.Fields(), "an entity whose name extends another keeps its own fields")

	secrets.ForgetEntity("e")
	recheck, err = secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"e.y.z"}, recheck.Fields())
}

func TestResolveEntityFailureNamesField(t *testing.T) {
	secrets := NewSecrets()
	_, err := secrets.ResolveEntity(t.Context(), "e", map[string]string{"e.key": "${vault:missing}"})
	var secretErr *SecretError
	require.ErrorAs(t, err, &secretErr)
	assert.Equal(t, "e.key", secretErr.Field)
	assert.Equal(t, "vault", secretErr.Scheme)
	require.ErrorIs(t, err, ErrUnknownSecretScheme)
}

func TestSecretRecheckSelectCommitsOnlySelected(t *testing.T) {
	vault := newFakeVault(map[string]string{"a": "one", "b": "two"})
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	ctx := t.Context()
	_, err := secrets.resolveField(ctx, "x", "${vault:a}")
	require.NoError(t, err)
	_, err = secrets.resolveField(ctx, "y", "${vault:b}")
	require.NoError(t, err)

	vault.set("a", "a2")
	vault.set("b", "b2")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	selected := recheck.Select(func(field string) bool { return field == "x" })
	assert.Equal(t, []string{"x"}, selected.Fields())
	selected.Commit()

	recheck, err = secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"y"}, recheck.Fields())
}

func TestOnlySecretReferences(t *testing.T) {
	for value, want := range map[string]bool{
		"${vault:a}":             true,
		"${vault:a}${env:B}":     true,
		"":                       false,
		"literal":                false,
		"Bearer ${env:TOKEN}":    false,
		"sk-literal-${env:TAIL}": false,
		"${env:A} ":              false,
		"$${env:A}":              false,
		"${LEGACY}":              false,
	} {
		assert.Equal(t, want, OnlySecretReferences(value), value)
	}
}

func TestEntityOwnsAnyMatchesExactFields(t *testing.T) {
	secrets := NewSecrets()
	require.NoError(t, secrets.Register("vault", newFakeVault(map[string]string{"a": "1", "b": "2"})))
	for entity, field := range map[string]string{
		"guardrail_definitions.pii":      "guardrail_definitions.pii.config.api_key",
		"guardrail_definitions.pii.team": "guardrail_definitions.pii.team.config.api_key",
	} {
		resolved, err := secrets.ResolveEntity(t.Context(), entity, map[string]string{field: "${vault:a}"})
		require.NoError(t, err)
		resolved.Record()
	}

	team := []string{"guardrail_definitions.pii.team.config.api_key"}
	assert.True(t, secrets.EntityOwnsAny("guardrail_definitions.pii.team", team))
	assert.False(t, secrets.EntityOwnsAny("guardrail_definitions.pii", team))
	assert.False(t, secrets.EntityOwnsAny("guardrail_definitions.pii", nil))
	assert.False(t, (*Secrets)(nil).EntityOwnsAny("guardrail_definitions.pii", team))

	secrets.ForgetEntity("guardrail_definitions.pii.team")
	assert.False(t, secrets.EntityOwnsAny("guardrail_definitions.pii.team", team))
}
