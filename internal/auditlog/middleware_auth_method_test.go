package auditlog

import (
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/require"
)

func TestEnrichEntryWithAuthMethodTrimsAndValidatesIdentifiers(t *testing.T) {
	c, _ := echotest.Get(t, "/")
	entry := &LogEntry{}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithAuthMethod(c, "  API_KEY  ")
	require.Equal(t, AuthMethodAPIKey, entry.AuthMethod)

	EnrichEntryWithAuthMethod(c, "master_key")
	require.Equal(t, AuthMethodMasterKey, entry.AuthMethod)

	EnrichEntryWithAuthMethod(c, "no_key")
	require.Equal(t, AuthMethodNoKey, entry.AuthMethod)

	EnrichEntryWithAuthMethod(c, "unknown")
	require.Equal(t, "unknown", entry.AuthMethod)

	EnrichEntryWithAuthMethod(c, "  OAuth  ")
	require.Equal(t, "oauth", entry.AuthMethod)
}

func TestEnrichEntryWithAuthMethodIgnoresBlankAndUnsafeValues(t *testing.T) {
	c, _ := echotest.Get(t, "/")
	entry := &LogEntry{}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithAuthMethod(c, "   ")
	require.Empty(t, entry.AuthMethod)

	EnrichEntryWithAuthMethod(c, "oauth\nsecret")
	require.Empty(t, entry.AuthMethod)

	EnrichEntryWithAuthMethod(c, strings.Repeat("a", 65))
	require.Empty(t, entry.AuthMethod)
}

func TestEnrichEntryWithPrincipalIDTrimsAndPreservesExistingValue(t *testing.T) {
	c, _ := echotest.Get(t, "/")
	entry := &LogEntry{PrincipalID: "existing"}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithPrincipalID(c, "   ")
	require.Equal(t, "existing", entry.PrincipalID)

	EnrichEntryWithPrincipalID(c, "  oidc:principal-1  ")
	require.Equal(t, "oidc:principal-1", entry.PrincipalID)

	withoutEntry, _ := echotest.Get(t, "/")
	EnrichEntryWithPrincipalID(withoutEntry, "ignored")
}
