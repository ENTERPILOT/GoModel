package users

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type testCatalog []string

func (c testCatalog) ProviderNames() []string { return []string(c) }

func TestNormalizeAllowedModels(t *testing.T) {
	t.Parallel()
	catalog := testCatalog{"openai", "anthropic"}

	tests := []struct {
		name    string
		raw     []string
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: nil, want: nil},
		{name: "blank entries dropped", raw: []string{" ", ""}, want: nil},
		{name: "exact", raw: []string{" openai/gpt-4o "}, want: []string{"openai/gpt-4o"}},
		{name: "provider wildcard", raw: []string{"anthropic/*"}, want: []string{"anthropic/"}},
		{name: "provider trailing slash", raw: []string{"anthropic/"}, want: []string{"anthropic/"}},
		{name: "global star", raw: []string{"*"}, want: []string{"/"}},
		{name: "model wide", raw: []string{"gpt-4o"}, want: []string{"gpt-4o"}},
		{name: "duplicates collapse", raw: []string{"anthropic/*", "anthropic/", "gpt-4o", "gpt-4o"}, want: []string{"anthropic/", "gpt-4o"}},
		{name: "unknown provider", raw: []string{"nope/*"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeAllowedModels(catalog, tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				require.True(t, IsValidationError(err))

				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got, "NormalizeAllowedModels(%v) = %v, want %v", tc.raw, got, tc.want)
		})
	}
}

func TestMatches(t *testing.T) {
	t.Parallel()
	gpt := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	claude := core.ModelSelector{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	groqOSS := core.ModelSelector{Provider: "groq", Model: "openai/gpt-oss-120b"}

	tests := []struct {
		name     string
		allowed  []string
		selector core.ModelSelector
		want     bool
	}{
		{name: "empty allows all", allowed: nil, selector: gpt, want: true},
		{name: "global", allowed: []string{"/"}, selector: claude, want: true},
		{name: "exact match", allowed: []string{"openai/gpt-4o"}, selector: gpt, want: true},
		{name: "exact mismatch", allowed: []string{"openai/gpt-4o"}, selector: claude, want: false},
		{name: "provider wide match", allowed: []string{"anthropic/"}, selector: claude, want: true},
		{name: "provider wide mismatch", allowed: []string{"anthropic/"}, selector: gpt, want: false},
		{name: "model wide match", allowed: []string{"gpt-4o"}, selector: gpt, want: true},
		{name: "model wide mismatch", allowed: []string{"gpt-4o"}, selector: claude, want: false},
		{name: "slash model exact", allowed: []string{"groq/openai/gpt-oss-120b"}, selector: groqOSS, want: true},
		{name: "any of several", allowed: []string{"anthropic/", "openai/gpt-4o"}, selector: gpt, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Matches(tc.allowed, tc.selector)
			require.Equal(t, tc.want, got, "Matches(%v, %s) = %v, want %v", tc.allowed, tc.selector.QualifiedModel(), got, tc.want)
		})
	}
}
