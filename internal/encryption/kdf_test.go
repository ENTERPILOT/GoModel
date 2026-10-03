package encryption

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKDFParams(t *testing.T) {
	got, err := parseKDFParams(defaultKDFParams.String())
	require.NoError(t, err)
	assert.Equal(t, defaultKDFParams, got)

	for _, bad := range []string{
		"",
		"m=19456",
		"m=0,t=2,p=1",
		"m=19456,t=0,p=1",
		"m=19456,t=2,p=0",
		"m=4194304,t=2,p=1", // 4 GiB: a tampered row must not exhaust memory
		"m=19456,t=100,p=1",
		"m=19456,t=2,p=64",
	} {
		_, err := parseKDFParams(bad)
		assert.Error(t, err, "params %q", bad)
	}
}

func TestLocalKEKDerivesPerSalt(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a := defaultKDFParams.derive("secret", salt)
	assert.Len(t, a, dekSize)
	assert.Equal(t, a, defaultKDFParams.derive("secret", salt), "derivation is deterministic")
	assert.NotEqual(t, a, defaultKDFParams.derive("secret", []byte("fedcba9876543210")))
	assert.NotEqual(t, a, defaultKDFParams.derive("other", salt))
}
