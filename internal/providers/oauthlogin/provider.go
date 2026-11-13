// Package oauthlogin serves an upstream account that was signed in with the
// OAuth 2.0 device authorization grant (RFC 8628) as an OpenAI-compatible
// provider.
//
// One provider type covers every supported account kind. The kind, chosen in
// the provider's configuration, selects the device, token, and inference
// endpoints; each endpoint is itself configuration, so an upstream that moves
// its URLs needs no code change. The operator starts a login from the admin
// API, approves the device code out of band, and the gateway polls the token
// endpoint until the approval lands. The resulting tokens are stored in the
// provider credential store, the same store every other provider secret uses.
//
// Requests go out with the access token. An expired access token is refreshed
// and the rotated tokens are written back. A refresh token the authorization
// server rejects ends the grant: the provider reports that a fresh login is
// required and never falls back to another credential.
package oauthlogin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

// providerType is the registered provider type and the credential-type name
// the admin API lists.
const providerType = "oauthlogin"

// defaultClientID is the public client id sent when the operator configured
// none. Device authorization clients are public (RFC 8628 §3.1), so the
// default only has to be stable and overridable.
const defaultClientID = "gomodel"

// Registration provides factory registration for the device-login provider.
// The "oauthlogin" type derives OAUTHLOGIN_API_KEY, OAUTHLOGIN_BASE_URL, and
// OAUTHLOGIN_MODELS by convention. The API key is optional: a device login
// supplies the credential, and a pasted access token remains a fallback for a
// deployment that signs in out of band.
var Registration = providers.Registration{
	Type: providerType,
	New:  New,
	Discovery: providers.DiscoveryConfig{
		AllowAPIKeyless: true,
		CredentialFields: []providers.CredentialField{
			{Name: providers.CredentialFieldAPIKeys, Advanced: true},
			{Name: providers.CredentialFieldBaseURL, Advanced: true},
			{Name: providers.CredentialFieldBackend, Options: accountOptions()},
			{Name: providers.CredentialFieldAuthType, Advanced: true},
			{Name: providers.CredentialFieldAPIVersion, Advanced: true},
			{Name: providers.CredentialFieldAPIMode, Advanced: true},
			// The device-login session, stored in the credential's existing secret
			// field. The admin login writes it; the form never asks for it.
			{Name: providers.CredentialFieldServiceAccountJSON, Advanced: true},
		},
	},
}

// Provider serves one signed-in account. The embedded adapter performs the
// OpenAI-compatible transport; auth headers come from the device-login
// session rather than from the adapter's own keyring.
type Provider struct {
	*openai.ChatCompatible
	name   string
	kind   accountKind
	client *grantClient
	store  credentialWriter

	mu      sync.Mutex
	session *session
	login   *loginPoll
}

var _ core.Provider = (*Provider)(nil)

// loginPoll is one device login waiting for the user to approve it.
type loginPoll struct {
	done   chan struct{}
	auth   DeviceAuthorization
	tokens tokenSet
	err    error
}

var (
	// credentialStore persists completed logins. It is process-wide because
	// the provider constructor only receives the provider configuration; the
	// admin wiring that owns the store registers it at startup.
	credentialStoreMu sync.RWMutex
	credentialStore   credentialWriter
)

// SetCredentialStore registers the store a completed device login writes its
// tokens to. nil disables persistence, which keeps tokens in memory for the
// life of the process.
func SetCredentialStore(store credentialWriter) {
	credentialStoreMu.Lock()
	defer credentialStoreMu.Unlock()
	credentialStore = store
}

func currentCredentialStore() credentialWriter {
	credentialStoreMu.RLock()
	defer credentialStoreMu.RUnlock()
	return credentialStore
}

// New creates an oauthlogin provider. The account kind and its endpoints come
// from the provider configuration; a stored device-login session in the
// credential supplies the tokens.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	kind, err := resolveAccount(cfg.Backend, cfg.BaseURL, cfg.APIMode, cfg.APIVersion, cfg.AuthType)
	if err != nil {
		return &unconfiguredProvider{reason: err.Error()}
	}
	if err := validateAccount(kind); err != nil {
		// An unusable configuration still builds: failing the request, with
		// the reason, beats panicking inside the factory.
		return &unconfiguredProvider{reason: err.Error()}
	}
	stored, err := parseSession(cfg.ServiceAccountJSON)
	if err != nil {
		return &unconfiguredProvider{reason: err.Error()}
	}
	name := opts.ClientName(providerType)
	client := newGrantClient(opts.HTTPClient, kind, firstNonEmpty(stored.ClientID, defaultClientID))
	p := &Provider{
		name:   name,
		kind:   kind,
		client: client,
		store:  currentCredentialStore(),
	}
	p.session = newSession(stored, client, func(ctx context.Context, updated storedSession) error {
		return p.persist(ctx, name, updated)
	})
	p.ChatCompatible = openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: providerType,
		BaseURL:      kind.baseURL,
		SetHeaders:   p.authorize,
	})
	return p
}

// authorize attaches the device-login access token to one upstream request.
// An expired token is refreshed first. With no session yet, it falls back to
// the configured API key, so a deployment that signs in out of band keeps
// working; once a login exists, only that login's tokens are used.
func (p *Provider) authorize(req *http.Request, apiKey string) {
	token, err := p.session.accessToken(req.Context())
	switch {
	case err == nil:
		req.Header.Set("Authorization", "Bearer "+token)
	case !p.session.signedIn() && strings.TrimSpace(apiKey) != "":
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	default:
		// No usable credential. Nothing is attached: a signed-in account whose
		// refresh was rejected must not fall back to another credential. The
		// chat methods fail the request before it is sent.
		slog.Warn("oauthlogin request has no usable access token", "provider", p.name, "error", errLoginRequired)
	}
}

// authenticate resolves the credential for one inference request, refreshing
// an expired access token first. A rejected refresh token fails the request
// here, so it never reaches the inference endpoint and never falls back to
// another credential.
func (p *Provider) authenticate(ctx context.Context) error {
	if _, err := p.session.accessToken(ctx); err != nil {
		return core.NewAuthenticationError(providerType, err.Error())
	}
	return nil
}

// ChatCompletion serves a chat request with the device-login access token.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.authenticate(ctx); err != nil {
		return nil, err
	}
	return p.ChatCompatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion is ChatCompletion for a streaming request.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if err := p.authenticate(ctx); err != nil {
		return nil, err
	}
	return p.ChatCompatible.StreamChatCompletion(ctx, req)
}

// persist writes a rotated token set back to the credential store. A failure
// is logged and swallowed by the session: the refreshed token is already in
// memory, and the next refresh tries again.
func (p *Provider) persist(ctx context.Context, providerName string, updated storedSession) error {
	if err := persistSession(ctx, p.store, providerName, updated); err != nil {
		slog.Warn("oauthlogin token refresh could not be stored", "provider", providerName, "error", err)
		return err
	}
	return nil
}

// StartLogin begins a device authorization grant. The returned authorization
// tells the operator where to approve it and which code to enter. The grant
// is polled in the background until the user approves, the code expires, or
// the grant is rejected. Starting a login replaces one that is still pending.
func (p *Provider) StartLogin(ctx context.Context) (DeviceAuthorization, error) {
	auth, err := p.client.authorize(ctx)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	poll := &loginPoll{done: make(chan struct{}), auth: auth}
	p.mu.Lock()
	p.login = poll
	p.mu.Unlock()
	go p.complete(poll)
	return auth, nil
}

// complete polls the token endpoint and, once the user approves, installs the
// tokens on the provider and in the credential store.
func (p *Provider) complete(poll *loginPoll) {
	p.finish(poll)
	// Closed after the result is written: Login reads it the moment the
	// channel closes, so the write has to be visible already.
	close(poll.done)
}

// finish polls the token endpoint and records the outcome on poll.
func (p *Provider) finish(poll *loginPoll) {
	tokens, err := p.client.pollToken(context.Background(), poll.auth)
	if err != nil {
		poll.err = err
		return
	}
	next := storedSession{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    tokens.TokenType,
		Expiry:       expiryFrom(time.Now(), tokens.ExpiresIn),
		Account:      p.kind.name,
		ClientID:     p.client.clientID,
	}
	p.session.replace(next)
	if err := persistSession(context.Background(), p.store, p.name, next); err != nil {
		slog.Warn("oauthlogin device login could not be stored", "provider", p.name, "error", err)
		poll.err = err
		return
	}
	poll.tokens = tokens
}

// LoginResult is the outcome of a device login. Pending reports that the user
// has not approved the code yet; the authorization is repeated so the caller
// can show it again.
type LoginResult struct {
	Pending       bool
	Authorization DeviceAuthorization
	Err           error
}

// Login reports the device login last started on this provider.
func (p *Provider) Login() LoginResult {
	p.mu.Lock()
	poll := p.login
	p.mu.Unlock()
	if poll == nil {
		return LoginResult{Err: errNoLogin}
	}
	select {
	case <-poll.done:
		return LoginResult{Authorization: poll.auth, Err: poll.err}
	default:
		return LoginResult{Pending: true, Authorization: poll.auth}
	}
}

// errNoLogin reports that no device login was started for the provider.
var errNoLogin = errorsNew("no device login has been started for this provider")

// unconfiguredProvider stands in for an instance whose configuration cannot
// serve traffic. Every surface reports the same reason.
type unconfiguredProvider struct{ reason string }

func (p *unconfiguredProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, p.err()
}

func (p *unconfiguredProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, p.err()
}

func (p *unconfiguredProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, p.err()
}

func (p *unconfiguredProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, p.err()
}

func (p *unconfiguredProvider) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, p.err()
}

func (p *unconfiguredProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, p.err()
}

func (p *unconfiguredProvider) err() error {
	return core.NewAuthenticationError(providerType, p.reason)
}

// errorsNew isolates the one place this file builds a plain error, so the
// text stays greppable.
func errorsNew(text string) error { return &loginError{text: text} }

type loginError struct{ text string }

func (e *loginError) Error() string { return e.text }
