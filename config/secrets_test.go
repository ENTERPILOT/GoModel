package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretsHasReference(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"plain", false},
		{"${env:TOKEN}", true},
		{"${file:/run/secrets/key}", true},
		{"${vault:prod/llm#openai@3}", true},
		{"${aws+sm.v2-x:name}", true},
		{"Bearer ${env:TOKEN}", true},
		{"http://u:${file:/x}@h:3128", true},
		{"${TOKEN}", false},
		{"${TOKEN:-default}", false},
		{"${foo:-bar}", false},
		{"${env:}", false},
		{"${Env:TOKEN}", false},
		{"${1env:TOKEN}", false},
		{"${env_x:TOKEN}", false},
		{"${env:TOKEN", false},
		{"$${env:TOKEN}", false},
		{"$${env:A} ${env:B}", true},
		{"${:x}", false},
	}
	var secrets *Secrets
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, secrets.HasReference(tt.value))
		})
	}
}

func TestHasUnresolvedPlaceholder(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"plain", false},
		{"${env:TOKEN}", false},
		{"Bearer ${env:TOKEN}", false},
		{"$${VAR}", false},
		{"${VAR}", true},
		{"${VAR:-default}", true},
		{"${env:TOKEN}-${VAR}", true},
		{"a${env:X", true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, HasUnresolvedPlaceholder(tt.value))
		})
	}
}

func TestSecretsResolve(t *testing.T) {
	dir := t.TempDir()
	passFile := filepath.Join(dir, "pass")
	require.NoError(t, os.WriteFile(passFile, []byte("s3cret\n"), 0o600))
	t.Setenv("GOMODEL_TEST_TOKEN", "tok")
	t.Setenv("GOMODEL_TEST_NESTED", "${env:GOMODEL_TEST_TOKEN}")

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "no reference", value: "plain", want: "plain"},
		{name: "env", value: "${env:GOMODEL_TEST_TOKEN}", want: "tok"},
		{name: "file", value: "${file:" + passFile + "}", want: "s3cret"},
		{name: "embedded", value: "Bearer ${env:GOMODEL_TEST_TOKEN}", want: "Bearer tok"},
		{name: "several", value: "http://u:${file:" + passFile + "}@h/${env:GOMODEL_TEST_TOKEN}", want: "http://u:s3cret@h/tok"},
		{name: "escape", value: "$${env:GOMODEL_TEST_TOKEN}", want: "${env:GOMODEL_TEST_TOKEN}"},
		{name: "escape next to a reference", value: "$${x}${env:GOMODEL_TEST_TOKEN}", want: "${x}tok"},
		{name: "legacy placeholder kept", value: "${UNSET_VAR}", want: "${UNSET_VAR}"},
		{name: "legacy default form kept", value: "${foo:-bar}", want: "${foo:-bar}"},
		{name: "unterminated kept", value: "a${env:X", want: "a${env:X"},
		{name: "resolved value not rescanned", value: "${env:GOMODEL_TEST_NESTED}", want: "${env:GOMODEL_TEST_TOKEN}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewSecrets().Resolve(t.Context(), tt.value)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSecretsResolveErrors(t *testing.T) {
	t.Setenv("GOMODEL_TEST_EMPTY", "")
	tests := []struct {
		name        string
		value       string
		wantScheme  string
		wantContain string
		wantIs      error
	}{
		{name: "unset env", value: "${env:GOMODEL_TEST_UNSET}", wantScheme: "env", wantContain: "GOMODEL_TEST_UNSET is not set"},
		{name: "empty env", value: "${env:GOMODEL_TEST_EMPTY}", wantScheme: "env", wantContain: "GOMODEL_TEST_EMPTY is empty"},
		{name: "relative file", value: "${file:secrets/key}", wantScheme: "file", wantContain: "must be absolute"},
		{name: "vault without Pro", value: "${vault:prod/openai}", wantScheme: "vault", wantContain: "GoModel Pro vaults (extensions.vaults)", wantIs: ErrUnknownSecretScheme},
		{name: "unknown scheme", value: "x ${aws:key}", wantScheme: "aws", wantContain: "built-in schemes are env and file", wantIs: ErrUnknownSecretScheme},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSecrets().Resolve(t.Context(), tt.value)
			require.Error(t, err)
			secretErr, ok := errors.AsType[*SecretError](err)
			require.True(t, ok, "error %v is not a *SecretError", err)
			assert.Equal(t, tt.wantScheme, secretErr.Scheme)
			assert.Contains(t, err.Error(), tt.wantContain)
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}

func TestSecretsRegister(t *testing.T) {
	resolver := SecretResolverFunc(func(context.Context, string) (string, error) { return "v", nil })
	tests := []struct {
		name     string
		scheme   string
		resolver SecretResolver
		wantErr  string
	}{
		{name: "env reserved", scheme: "env", resolver: resolver, wantErr: "reserved"},
		{name: "file reserved", scheme: "file", resolver: resolver, wantErr: "reserved"},
		{name: "invalid scheme", scheme: "Vault", resolver: resolver, wantErr: "must match"},
		{name: "empty scheme", scheme: "", resolver: resolver, wantErr: "must match"},
		{name: "nil resolver", scheme: "vault", resolver: nil, wantErr: "nil resolver"},
		{name: "valid", scheme: "vault", resolver: resolver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewSecrets().Register(tt.scheme, tt.resolver)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}

	t.Run("duplicate", func(t *testing.T) {
		secrets := NewSecrets()
		require.NoError(t, secrets.Register("vault", resolver))
		require.ErrorContains(t, secrets.Register("vault", resolver), "already registered")
	})
	t.Run("nil Secrets", func(t *testing.T) {
		var secrets *Secrets
		require.Error(t, secrets.Register("vault", resolver))
	})
	t.Run("zero value", func(t *testing.T) {
		var secrets Secrets
		require.NoError(t, secrets.Register("vault", resolver))
		got, err := secrets.Resolve(t.Context(), "${vault:x}")
		require.NoError(t, err)
		assert.Equal(t, "v", got)
	})
}

func TestSecretsRegisteredResolver(t *testing.T) {
	secrets := NewSecrets()
	var gotReference string
	require.NoError(t, secrets.Register("vault", SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		gotReference = reference
		if reference == "prod/missing" {
			return "", errors.New("secret prod/missing not found")
		}
		return "sk-from-vault", nil
	})))

	got, err := secrets.Resolve(t.Context(), "${vault:prod/llm#openai@3}")
	require.NoError(t, err)
	assert.Equal(t, "sk-from-vault", got)
	assert.Equal(t, "prod/llm#openai@3", gotReference)

	_, err = secrets.Resolve(t.Context(), "${vault:prod/missing}")
	require.ErrorContains(t, err, "secret reference ${vault:...}: secret prod/missing not found")
}

func TestSecretsConcurrentRegisterAndResolve(t *testing.T) {
	secrets := NewSecrets()
	t.Setenv("GOMODEL_TEST_TOKEN", "tok")
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			scheme := "s" + strings.Repeat("x", i)
			errs <- secrets.Register(scheme, SecretResolverFunc(func(context.Context, string) (string, error) { return "v", nil }))
			_, err := secrets.Resolve(context.Background(), "${env:GOMODEL_TEST_TOKEN} ${"+scheme+":ref}")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestResolveFileSecret(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		return path
	}
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr string
	}{
		{name: "no newline", path: write("plain", []byte("abc")), want: "abc"},
		{name: "one newline stripped", path: write("lf", []byte("abc\n")), want: "abc"},
		{name: "crlf stripped", path: write("crlf", []byte("abc\r\n")), want: "abc"},
		{name: "only one newline stripped", path: write("lflf", []byte("abc\n\n")), want: "abc\n"},
		{name: "inner newlines kept", path: write("multi", []byte("{\n  \"k\": 1\n}\n")), want: "{\n  \"k\": 1\n}"},
		{name: "empty file", path: write("empty", nil), want: ""},
		{name: "exactly 1 MiB", path: write("max", []byte(strings.Repeat("a", maxSecretFileSize))), want: strings.Repeat("a", maxSecretFileSize)},
		{name: "over 1 MiB", path: write("big", []byte(strings.Repeat("a", maxSecretFileSize+1))), wantErr: "larger than 1 MiB"},
		{name: "missing", path: filepath.Join(dir, "missing"), wantErr: "no such file"},
		{name: "directory", path: dir, wantErr: "read " + dir},
		{name: "relative", path: "relative/path", wantErr: "must be absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveFileSecret(t.Context(), tt.path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandStringLeavesSecretReferences(t *testing.T) {
	t.Setenv("GOMODEL_TEST_TOKEN", "tok")
	// A variable literally named like a reference must not be looked up.
	t.Setenv("env:GOMODEL_TEST_TOKEN", "coincidence")
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "env reference", input: "${env:GOMODEL_TEST_TOKEN}", want: "${env:GOMODEL_TEST_TOKEN}"},
		{name: "file reference with :- inside", input: "${file:/run/a:-b}", want: "${file:/run/a:-b}"},
		{name: "embedded next to legacy", input: "${GOMODEL_TEST_TOKEN}:${env:X}", want: "tok:${env:X}"},
		{name: "escape kept for the field pass", input: "$${env:X}", want: "$${env:X}"},
		{name: "escaped legacy kept", input: "$${GOMODEL_TEST_TOKEN}", want: "$${GOMODEL_TEST_TOKEN}"},
		{name: "double dollar kept", input: "pa$$word", want: "pa$$word"},
		{name: "legacy default still applies", input: "${foo:-bar}", want: "bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, expandString(tt.input))
		})
	}
}
