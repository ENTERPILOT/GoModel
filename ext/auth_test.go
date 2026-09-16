package ext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthenticationContextRoundTripClonesLabels(t *testing.T) {
	authentication := Authentication{
		PrincipalID: "oidc:principal-1",
		UserPath:    "/users/one",
		Labels:      []string{"team-a"},
		Method:      "oidc",
	}
	ctx := WithAuthentication(t.Context(), authentication)
	authentication.Labels[0] = "mutated-source"

	got, ok := AuthenticationFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "oidc:principal-1", got.PrincipalID)
	require.Equal(t, "team-a", got.Labels[0], "authentication = %+v", got)

	got.Labels[0] = "mutated-result"

	again, ok := AuthenticationFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "team-a", again.Labels[0], "stored authentication was mutated: %+v", again)
}

func TestWithoutAuthenticationHidesInheritedIdentity(t *testing.T) {
	ctx := WithAuthentication(t.Context(), Authentication{
		PrincipalID: "oidc:ambient",
		UserPath:    "/users/ambient",
		Labels:      []string{"sso"},
	})
	ctx = WithoutAuthentication(ctx)
	authentication, ok := AuthenticationFromContext(ctx)
	require.False(t, ok, "AuthenticationFromContext() = %+v, true; want no identity", authentication)
}

func TestAuthenticationFromContextHandlesMissingContext(t *testing.T) {
	_, ok := AuthenticationFromContext(nil) //nolint:staticcheck // Exercise the helper's defensive nil-context branch.
	require.False(t, ok, "nil context returned an authentication")
	_, ok = AuthenticationFromContext(context.Background())
	require.False(t, ok)
}
