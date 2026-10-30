package core

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredentialHeaderList is the coverage the list owes: one case per header the
// gateway treats as a secret. The length check ties the table to the map, so a
// header added to the list without a case — or a case for a header that is not in
// the list — fails here.
func TestCredentialHeaderList(t *testing.T) {
	names := []string{
		"authorization",
		"proxy-authorization",
		"cookie",
		"set-cookie",
		"x-api-key",
		"api-key",             // Azure OpenAI
		"x-goog-api-key",      // Google Gemini / Vertex
		"x-anthropic-api-key", //
		"x-auth-token",
		"x-access-token",
		"x-gomodel-key",
		"xi-api-key", // ElevenLabs
	}
	require.Len(t, credentialHeaders, len(names), "credential list and cases drifted apart")

	for _, name := range names {
		// Clients and providers spell the header however they like; the scratch
		// buffer in IsCredentialHeader must fold all of them, and must be long
		// enough for every name in the list.
		for _, variant := range []string{name, strings.ToUpper(name), http.CanonicalHeaderKey(name), " " + name + " "} {
			assert.True(t, IsCredentialHeader(variant), "IsCredentialHeader(%q)", variant)
		}
	}
}

func TestIsCredentialHeader(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "authorization", want: true},
		{name: "Authorization", want: true},
		{name: " X-API-Key ", want: true},
		{name: "Set-Cookie", want: true},
		{name: "X-Anthropic-Api-Key", want: true},
		{name: "x-gomodel-key", want: true},
		{name: "X-Team", want: false},
		{name: "Content-Type", want: false},
		{name: "", want: false},
	}
	for _, tt := range tests {
		got := IsCredentialHeader(tt.name)
		assert.Equal(t, tt.want, got, "IsCredentialHeader(%q)", tt.name)
	}
}
