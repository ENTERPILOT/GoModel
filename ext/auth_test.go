package ext

import (
	"context"
	"testing"
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
	if !ok {
		t.Fatal("authentication missing from context")
	}
	if got.PrincipalID != "oidc:principal-1" || got.Labels[0] != "team-a" {
		t.Fatalf("authentication = %+v", got)
	}
	got.Labels[0] = "mutated-result"

	again, ok := AuthenticationFromContext(ctx)
	if !ok || again.Labels[0] != "team-a" {
		t.Fatalf("stored authentication was mutated: %+v", again)
	}
}

func TestWithoutAuthenticationHidesInheritedIdentity(t *testing.T) {
	ctx := WithAuthentication(t.Context(), Authentication{
		PrincipalID: "oidc:ambient",
		UserPath:    "/users/ambient",
		Labels:      []string{"sso"},
	})
	ctx = WithoutAuthentication(ctx)

	if authentication, ok := AuthenticationFromContext(ctx); ok {
		t.Fatalf("AuthenticationFromContext() = %+v, true; want no identity", authentication)
	}
}

func TestAuthenticationFromContextHandlesMissingContext(t *testing.T) {
	if _, ok := AuthenticationFromContext(nil); ok { //nolint:staticcheck // Exercise the helper's defensive nil-context branch.
		t.Fatal("nil context returned an authentication")
	}
	if _, ok := AuthenticationFromContext(context.Background()); ok {
		t.Fatal("empty context returned an authentication")
	}
}
