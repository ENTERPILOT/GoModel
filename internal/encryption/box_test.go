package encryption

import (
	"bytes"
	"crypto/rand"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBox(t *testing.T, active string, ids ...string) *Box {
	t.Helper()
	deks := map[string][]byte{}
	for _, id := range append([]string{active}, ids...) {
		dek := make([]byte, dekSize)
		_, err := rand.Read(dek)
		require.NoError(t, err)
		deks[id] = dek
	}
	box, err := newBox(active, deks)
	require.NoError(t, err)
	return box
}

func TestBoxSealOpenRoundTrip(t *testing.T) {
	box := testBox(t, "1")
	aad := AAD("provider_credential", "openai", "api_keys")

	sealed, err := box.Seal(aad, "sk-secret")
	require.NoError(t, err)
	assert.Regexp(t, `^enc:v1:1:[A-Za-z0-9+/]+=*$`, sealed)
	assert.NotContains(t, sealed, "sk-secret")
	assert.True(t, IsSealed(sealed))
	assert.True(t, box.IsCurrent(sealed))

	again, err := box.Seal(aad, "sk-secret")
	require.NoError(t, err)
	assert.NotEqual(t, sealed, again, "every seal must use a fresh nonce")

	opened, err := box.Open(aad, sealed)
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", opened)
}

func TestBoxOpenRejects(t *testing.T) {
	box := testBox(t, "1")
	aad := AAD("mcp_server", "github", "headers.Authorization")
	sealed, err := box.Seal(aad, "Bearer x")
	require.NoError(t, err)

	tests := []struct {
		name  string
		aad   []byte
		value string
		want  string
	}{
		{"other entity", AAD("mcp_server", "gitlab", "headers.Authorization"), sealed, "moved from another row or field"},
		{"other field", AAD("mcp_server", "github", "headers.X-Token"), sealed, "moved from another row or field"},
		{"other kind", AAD("guardrail", "github", "headers.Authorization"), sealed, "moved from another row or field"},
		{"unknown key", aad, strings.Replace(sealed, "enc:v1:1:", "enc:v1:9:", 1), "not in encryption_keys"},
		{"no payload", aad, "enc:v1:1:", "malformed"},
		{"bad base64", aad, "enc:v1:1:!!!", "malformed"},
		{"too short", aad, "enc:v1:1:AAAA", "malformed"},
		{"tampered", aad, sealed[:len(sealed)-4] + "AAA=", "decryption failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := box.Open(tt.aad, tt.value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Empty(t, got)
		})
	}

	other := testBox(t, "1")
	_, err = other.Open(aad, sealed)
	require.Error(t, err, "a different data key with the same id must not open the value")
}

func TestBoxPlaintextPassesThrough(t *testing.T) {
	for name, box := range map[string]*Box{"enabled": testBox(t, "1"), "disabled": Disabled(), "nil": nil} {
		t.Run(name, func(t *testing.T) {
			opened, err := box.Open(AAD("k", "id", "f"), "legacy-plaintext")
			require.NoError(t, err)
			assert.Equal(t, "legacy-plaintext", opened)

			empty, err := box.Seal(AAD("k", "id", "f"), "")
			require.NoError(t, err)
			assert.Empty(t, empty, "empty values stay empty")
		})
	}
}

func TestDisabledBox(t *testing.T) {
	box := Disabled()
	assert.False(t, box.Enabled())
	assert.Empty(t, box.ActiveKeyID())

	sealed, err := box.Seal(AAD("k", "id", "f"), "secret")
	require.NoError(t, err)
	assert.Equal(t, "secret", sealed)

	_, err = box.Open(AAD("k", "id", "f"), "enc:v1:1:AAAA")
	require.ErrorIs(t, err, ErrKeyRequired)
	assert.False(t, box.NeedsReseal(Field{Name: "f", Value: &sealed}))
}

func TestBoxIsCurrentAndNeedsReseal(t *testing.T) {
	old := testBox(t, "1")
	sealedOld, err := old.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)

	box := testBox(t, "2", "1")
	sealedNew, err := box.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)
	plain, empty := "v", ""

	assert.False(t, box.IsCurrent(sealedOld))
	assert.True(t, box.IsCurrent(sealedNew))
	assert.False(t, box.IsCurrent(plain))

	tests := []struct {
		name  string
		value *string
		want  bool
	}{
		{"current", &sealedNew, false},
		{"empty", &empty, false},
		{"plaintext", &plain, true},
		{"older key", &sealedOld, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, box.NeedsReseal(Field{Name: "f", Value: tt.value}))
		})
	}
}

func TestSealOpenFields(t *testing.T) {
	box := testBox(t, "1")
	a, b := "alpha", ""
	fields := []Field{{Name: "a", Value: &a}, {Name: "b", Value: &b}}
	require.NoError(t, box.SealFields("kind", "row", fields...))
	assert.True(t, IsSealed(a))
	assert.Empty(t, b)

	require.NoError(t, box.OpenFields("kind", "row", fields...))
	assert.Equal(t, "alpha", a)

	require.NoError(t, box.SealFields("kind", "row", fields...))
	err := box.OpenFields("kind", "other-row", fields...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `kind "other-row" field a`)
	assert.NotContains(t, err.Error(), "alpha")
}

func TestPlaintextWarningLogsOnce(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	box := Disabled()
	for range 3 {
		_, err := box.Open(AAD("k", "id", "f"), "plaintext-secret")
		require.NoError(t, err)
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "stored in plaintext"))
	assert.Contains(t, logs.String(), "GOMODEL_ENCRYPTION_KEY")
	assert.NotContains(t, logs.String(), "plaintext-secret")

	logs.Reset()
	_, err := Disabled().Open(AAD("k", "id", "f"), "")
	require.NoError(t, err)
	assert.Empty(t, logs.String(), "empty values are not secrets")
}

func TestBoxReloadsKeysForUnknownKeyID(t *testing.T) {
	stale := testBox(t, "1")
	fresh := testBox(t, "2", "1")
	fresh.keys["1"] = stale.keys["1"]
	sealed, err := fresh.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)

	reloads := 0
	stale.reload = func() (*Box, error) {
		reloads++
		return fresh, nil
	}
	opened, err := stale.Open(AAD("k", "id", "f"), sealed)
	require.NoError(t, err)
	assert.Equal(t, "v", opened)
	assert.Equal(t, 1, reloads)
	assert.Equal(t, "2", stale.ActiveKeyID(), "new values are sealed with the rotated key")

	unknown := strings.Replace(sealed, "enc:v1:2:", "enc:v1:7:", 1)
	_, err = stale.Open(AAD("k", "id", "f"), unknown)
	require.ErrorIs(t, err, errUnknownKey)
	_, err = stale.Open(AAD("k", "id", "f"), unknown)
	require.ErrorIs(t, err, errUnknownKey)
	assert.Equal(t, 1, reloads, "reloads are rate limited")
}
