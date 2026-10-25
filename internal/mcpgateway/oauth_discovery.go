package mcpgateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/enterpilot/gomodel/internal/version"
)

// ErrOAuthClientRequired means the authorization server offers no dynamic
// client registration, so the operator has to enter a client ID registered
// with it for the gateway's redirect URI.
var ErrOAuthClientRequired = errors.New("the authorization server does not support dynamic client registration; register an OAuth client for the redirect URI and enter its client ID")

// oauthDiscovery is what MCP authorization discovery found for one server.
type oauthDiscovery struct {
	// resource is the canonical server URI from the protected resource
	// metadata, sent as the RFC 8707 resource indicator.
	resource string
	scopes   []string
	asm      *oauthex.AuthServerMeta
}

// discoverOAuth runs MCP authorization discovery for spec: an unauthenticated
// probe for the WWW-Authenticate challenge, protected resource metadata
// (RFC 9728), then authorization server metadata (RFC 8414 / OpenID
// Connect Discovery). It refuses servers without S256 PKCE support.
func discoverOAuth(ctx context.Context, client *http.Client, spec ServerSpec) (*oauthDiscovery, error) {
	challenges := probeChallenges(ctx, client, spec)
	prm, err := protectedResourceMetadata(ctx, client, challenges, spec.URL)
	if err != nil {
		return nil, err
	}
	issuer := prm.AuthorizationServers[0]
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil {
		return nil, fmt.Errorf("read authorization server metadata of %s: %w", issuer, err)
	}
	if asm == nil {
		return nil, fmt.Errorf("authorization server %s publishes no OAuth 2.0 or OpenID Connect metadata", issuer)
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return nil, fmt.Errorf("authorization server %s does not support S256 PKCE", issuer)
	}
	if asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return nil, fmt.Errorf("authorization server %s metadata lacks an authorization or token endpoint", issuer)
	}
	// Scope selection: the challenge's scope first, else every scope the
	// resource lists. offline_access asks for a refresh token when the
	// authorization server offers one.
	scopes := challengeScopes(challenges)
	if len(scopes) == 0 {
		scopes = slices.Clone(prm.ScopesSupported)
	}
	if slices.Contains(asm.ScopesSupported, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	return &oauthDiscovery{resource: prm.Resource, scopes: scopes, asm: asm}, nil
}

// probeChallenges sends one unauthenticated request the way the transport
// opens a session and returns the Bearer challenges of a 401 or 403 answer.
// It is best effort: discovery falls back to the well-known URIs.
func probeChallenges(ctx context.Context, client *http.Client, spec ServerSpec) []oauthex.Challenge {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	var req *http.Request
	var err error
	if spec.Transport == "sse" {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
		if err == nil {
			req.Header.Set("Accept", "text/event-stream")
		}
	} else {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"gomodel","version":"` + version.Version + `"}}}`
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, spec.URL, strings.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
		}
	}
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil
	}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return nil
	}
	return challenges
}

// protectedResourceMetadata tries the challenge's resource_metadata URL, then
// the well-known URI at the endpoint's path, then the one at the root, as
// the MCP authorization spec orders them.
func protectedResourceMetadata(ctx context.Context, client *http.Client, challenges []oauthex.Challenge, serverURL string) (*oauthex.ProtectedResourceMetadata, error) {
	var lastErr error
	for _, candidate := range protectedResourceMetadataURLs(challengeParam(challenges, "resource_metadata"), serverURL) {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, candidate.url, candidate.resource, client)
		if err != nil {
			lastErr = err
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, errors.New("protected resource metadata lists no authorization servers")
		}
		return prm, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("the server publishes no usable OAuth protected resource metadata: %w", redactDialError(lastErr))
	}
	return nil, errors.New("the server publishes no OAuth protected resource metadata")
}

type prmCandidate struct {
	url      string
	resource string
}

func protectedResourceMetadataURLs(metadataURL, serverURL string) []prmCandidate {
	var candidates []prmCandidate
	if metadataURL != "" {
		candidates = append(candidates, prmCandidate{url: metadataURL, resource: serverURL})
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return candidates
	}
	wellKnown := *parsed
	wellKnown.RawQuery = ""
	wellKnown.Fragment = ""
	if path := strings.Trim(parsed.Path, "/"); path != "" {
		wellKnown.Path = "/.well-known/oauth-protected-resource/" + path
		candidates = append(candidates, prmCandidate{url: wellKnown.String(), resource: serverURL})
	}
	wellKnown.Path = "/.well-known/oauth-protected-resource"
	root := url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	candidates = append(candidates, prmCandidate{url: wellKnown.String(), resource: root.String()})
	return candidates
}

func challengeParam(challenges []oauthex.Challenge, name string) string {
	for _, challenge := range challenges {
		if value := challenge.Params[name]; value != "" {
			return value
		}
	}
	return ""
}

func challengeScopes(challenges []oauthex.Challenge) []string {
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" && challenge.Params["scope"] != "" {
			return strings.Fields(challenge.Params["scope"])
		}
	}
	return nil
}

// oauthClient is the OAuth client the gateway authorizes as.
type oauthClient struct {
	id         string
	secret     string
	authStyle  oauth2.AuthStyle
	registered bool
}

// resolveOAuthClient picks the client in the spec's priority order: the
// pre-registered client the operator entered, the client stored from an
// earlier sign-in with the same authorization server and redirect URI, then
// dynamic client registration (RFC 7591).
func resolveOAuthClient(ctx context.Context, client *http.Client, asm *oauthex.AuthServerMeta, req OAuthStartRequest, stored *oauthRecord) (oauthClient, error) {
	if id := strings.TrimSpace(req.ClientID); id != "" {
		secret := strings.TrimSpace(req.ClientSecret)
		return oauthClient{id: id, secret: secret, authStyle: tokenAuthStyle(asm.TokenEndpointAuthMethodsSupported, secret)}, nil
	}
	if stored != nil && stored.ClientID != "" && sameIssuer(stored.Issuer, asm.Issuer) && stored.RedirectURI == req.RedirectURI {
		return oauthClient{id: stored.ClientID, secret: stored.ClientSecret, authStyle: oauth2.AuthStyle(stored.AuthStyle), registered: stored.Registered}, nil
	}
	if asm.RegistrationEndpoint == "" {
		return oauthClient{}, ErrOAuthClientRequired
	}
	registered, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{req.RedirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              "GoModel",
		SoftwareID:              "gomodel",
		SoftwareVersion:         version.Version,
		ApplicationType:         applicationType(req.RedirectURI),
	}, client)
	if err != nil {
		return oauthClient{}, registrationError(err)
	}
	return oauthClient{
		id:         registered.ClientID,
		secret:     registered.ClientSecret,
		authStyle:  registeredAuthStyle(registered.TokenEndpointAuthMethod, registered.ClientSecret),
		registered: true,
	}, nil
}

// registrationError keeps the RFC 7591 error and drops response bodies the
// SDK quotes, which for a malformed success response include the secret.
func registrationError(err error) error {
	if regErr, ok := errors.AsType[*oauthex.ClientRegistrationError](err); ok {
		if regErr.ErrorDescription != "" {
			return fmt.Errorf("dynamic client registration failed: %s: %s", regErr.ErrorCode, truncateOAuthText(regErr.ErrorDescription))
		}
		return fmt.Errorf("dynamic client registration failed: %s", regErr.ErrorCode)
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("dynamic client registration failed: %w", redactDialError(err))
	}
	return errors.New("dynamic client registration failed: the authorization server returned an unexpected response")
}

func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// applicationType is "native" for a loopback redirect URI and "web"
// otherwise, as OpenID Connect registration expects.
func applicationType(redirectURI string) string {
	parsed, err := url.Parse(redirectURI)
	if err == nil && isLoopbackHost(parsed.Hostname()) {
		return "native"
	}
	return "web"
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// tokenAuthStyle picks how a pre-registered client authenticates at the
// token endpoint: no secret means a public client.
func tokenAuthStyle(supported []string, secret string) oauth2.AuthStyle {
	if secret == "" {
		return oauth2.AuthStyleInParams
	}
	switch {
	case slices.Contains(supported, "client_secret_post"):
		return oauth2.AuthStyleInParams
	case slices.Contains(supported, "client_secret_basic"):
		return oauth2.AuthStyleInHeader
	}
	return oauth2.AuthStyleAutoDetect
}

func registeredAuthStyle(method, secret string) oauth2.AuthStyle {
	switch {
	case secret == "", method == "none", method == "client_secret_post":
		return oauth2.AuthStyleInParams
	default:
		// client_secret_basic is the RFC 7591 default.
		return oauth2.AuthStyleInHeader
	}
}

// resourceClient returns client with token requests to tokenURL carrying
// the RFC 8707 resource parameter. The oauth2 package adds it to the code
// exchange on request, but has no option for the refresh-token grant.
func resourceClient(client *http.Client, tokenURL, resource string) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone := *client
	clone.Transport = &resourceRoundTripper{base: base, tokenURL: tokenURL, resource: resource}
	return &clone
}

type resourceRoundTripper struct {
	base     http.RoundTripper
	tokenURL string
	resource string
}

func (t *resourceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.resource == "" || req.Method != http.MethodPost || req.Body == nil || req.URL.String() != t.tokenURL {
		return t.base.RoundTrip(req)
	}
	data, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, fmt.Errorf("token request body is not a form")
	}
	if form.Get("resource") == "" {
		form.Set("resource", t.resource)
	}
	encoded := form.Encode()
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewBufferString(encoded))
	clone.ContentLength = int64(len(encoded))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewBufferString(encoded)), nil }
	return t.base.RoundTrip(clone)
}
