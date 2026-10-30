package mcpgateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// OAuthCallbackPath is where authorization servers send the operator back
// after an MCP server sign-in. It sits outside the authenticated admin API:
// the browser arrives without the dashboard's bearer token, and the single-use
// state plus a browser-binding cookie authenticate the callback instead.
const OAuthCallbackPath = "/admin/mcp-oauth/callback"

// oauthFlowTTL bounds how long a started sign-in waits for its callback.
const oauthFlowTTL = 10 * time.Minute

// maxPendingOAuthFlows caps unfinished sign-ins kept in memory.
const maxPendingOAuthFlows = 64

var (
	// ErrOAuthUnavailable means the store cannot hold OAuth records.
	ErrOAuthUnavailable = errors.New("mcp oauth sign-in is unavailable without a store")
	// ErrOAuthInvalidRequest wraps sign-in input the caller has to fix.
	ErrOAuthInvalidRequest = errors.New("invalid mcp oauth request")
	// ErrOAuthFlowUnknown means the callback's state matches no pending
	// sign-in on this instance: it expired, was used, or never existed.
	ErrOAuthFlowUnknown = errors.New("the sign-in expired or was already completed; start it again from the dashboard")
)

// OAuthStartRequest starts an MCP server sign-in.
type OAuthStartRequest struct {
	// RedirectURI is the absolute URL of OAuthCallbackPath as the operator's
	// browser reaches the gateway.
	RedirectURI string
	// ClientID and ClientSecret name a client the operator registered with
	// the authorization server. Empty uses the stored client or dynamic
	// client registration.
	ClientID     string
	ClientSecret string
}

// OAuthStart is a started sign-in: the browser goes to AuthorizationURL with
// the binding cookie set, which the callback has to present.
type OAuthStart struct {
	AuthorizationURL string
	Binding          string
}

// OAuthCallback is the authorization response as the browser delivered it.
type OAuthCallback struct {
	State            string
	Code             string
	Issuer           string
	Error            string
	ErrorDescription string
	Binding          string
}

type oauthFlow struct {
	server       string
	binding      string
	verifier     string
	issuer       string
	issSupported bool
	config       *oauth2.Config
	record       oauthRecord
	expires      time.Time
}

// oauthManager holds the token sources of signed-in servers, the sign-ins in
// progress, and the last sign-in failure per server.
type oauthManager struct {
	store      *oauthStore
	httpClient *http.Client

	mu       sync.Mutex
	sources  map[string]*oauthSource
	pending  map[string]*oauthFlow
	failures map[string]string
}

func newOAuthManager(store *oauthStore, httpClient *http.Client) *oauthManager {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &oauthManager{
		store:      store,
		httpClient: httpClient,
		sources:    make(map[string]*oauthSource),
		pending:    make(map[string]*oauthFlow),
		failures:   make(map[string]string),
	}
}

// attach sets each HTTP spec's token source from the stored records: a new
// sign-in gets a new source, so the manager redials with it; tokens another
// instance refreshed are adopted in place. A server whose URL changed since
// sign-in does not get the tokens. keepMissing keeps the current source of a
// server whose record could not be read.
func (m *oauthManager) attach(specs []ServerSpec, records map[string]oauthRecord, keepMissing bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sources := make(map[string]*oauthSource, len(records))
	for i := range specs {
		spec := &specs[i]
		spec.oauth = nil
		if !oauthTransport(spec.Transport) {
			continue
		}
		current := m.sources[spec.Name]
		record, ok := records[spec.Name]
		switch {
		case !ok && keepMissing && current != nil && current.serverURL() == spec.URL:
			spec.oauth = current
		case !ok || record.ServerURL != spec.URL:
		case current != nil && current.sameSignIn(record):
			current.adopt(record)
			spec.oauth = current
		default:
			spec.oauth = newOAuthSource(spec.Name, record, m.store, m.httpClient)
		}
		if spec.oauth != nil {
			sources[spec.Name] = spec.oauth
		}
	}
	m.sources = sources
}

// view is the sign-in state of name: its source, else its last failure.
func (m *oauthManager) view(spec ServerSpec) *OAuthView {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	failure := m.failures[spec.Name]
	m.mu.Unlock()
	if spec.oauth != nil {
		view := spec.oauth.view()
		if failure != "" && view.LastError == "" {
			view.LastError = failure
		}
		return view
	}
	if failure != "" {
		return &OAuthView{Status: OAuthStatusError, LastError: failure}
	}
	return nil
}

func (m *oauthManager) fail(name string, err error) {
	m.mu.Lock()
	m.failures[name] = err.Error()
	m.mu.Unlock()
}

func (m *oauthManager) clearFailure(name string) {
	m.mu.Lock()
	delete(m.failures, name)
	m.mu.Unlock()
}

func (m *oauthManager) addFlow(state string, flow *oauthFlow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var oldest string
	for key, pending := range m.pending {
		if now.After(pending.expires) {
			delete(m.pending, key)
			continue
		}
		if oldest == "" || pending.expires.Before(m.pending[oldest].expires) {
			oldest = key
		}
	}
	if len(m.pending) >= maxPendingOAuthFlows && oldest != "" {
		delete(m.pending, oldest)
	}
	m.pending[state] = flow
}

// takeFlow removes and returns the pending flow for state, so a state works
// once.
func (m *oauthManager) takeFlow(state string) (*oauthFlow, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	flow, ok := m.pending[state]
	delete(m.pending, state)
	if !ok || time.Now().After(flow.expires) {
		return nil, false
	}
	return flow, true
}

func oauthTransport(transport string) bool {
	return transport == "" || transport == "http" || transport == "sse"
}

// StartOAuth runs MCP authorization discovery for server name and returns
// the authorization URL the operator's browser opens. Config-declared and
// dashboard-managed HTTP servers can both sign in: tokens are runtime state,
// not part of the server definition.
func (s *Service) StartOAuth(ctx context.Context, name string, req OAuthStartRequest) (OAuthStart, error) {
	if s.oauth == nil || s.oauth.store == nil {
		return OAuthStart{}, ErrOAuthUnavailable
	}
	spec, ok := s.manager.spec(name)
	if !ok {
		return OAuthStart{}, ErrNotFound
	}
	if !oauthTransport(spec.Transport) {
		return OAuthStart{}, fmt.Errorf("%w: OAuth sign-in needs an http or sse server", ErrOAuthInvalidRequest)
	}
	req.RedirectURI = strings.TrimSpace(req.RedirectURI)
	if err := ValidateOAuthRedirectURI(req.RedirectURI); err != nil {
		return OAuthStart{}, err
	}
	start, err := s.startOAuth(ctx, spec, req)
	if err != nil {
		s.oauth.fail(name, err)
		return OAuthStart{}, err
	}
	return start, nil
}

func (s *Service) startOAuth(ctx context.Context, spec ServerSpec, req OAuthStartRequest) (OAuthStart, error) {
	discovery, err := discoverOAuth(ctx, s.oauth.httpClient, spec)
	if err != nil {
		return OAuthStart{}, err
	}
	stored, err := s.oauth.store.get(ctx, spec.Name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		slog.Warn("stored mcp oauth record could not be read; registering a new client", "server", spec.Name, "error", err)
	}
	client, err := resolveOAuthClient(ctx, s.oauth.httpClient, discovery.asm, req, stored)
	if err != nil {
		return OAuthStart{}, err
	}
	record := oauthRecord{
		ServerURL:    spec.URL,
		Resource:     discovery.resource,
		Issuer:       discovery.asm.Issuer,
		TokenURL:     discovery.asm.TokenEndpoint,
		RedirectURI:  req.RedirectURI,
		ClientID:     client.id,
		ClientSecret: client.secret,
		AuthStyle:    int(client.authStyle),
		Registered:   client.registered,
		Scopes:       discovery.scopes,
	}
	config := record.config(discovery.asm.AuthorizationEndpoint)
	verifier := oauth2.GenerateVerifier()
	state := rand.Text()
	authURL := config.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", record.Resource),
	)
	binding := rand.Text()
	s.oauth.addFlow(state, &oauthFlow{
		server:       spec.Name,
		binding:      binding,
		verifier:     verifier,
		issuer:       discovery.asm.Issuer,
		issSupported: discovery.asm.AuthorizationResponseIssParameterSupported,
		config:       config,
		record:       record,
		expires:      time.Now().Add(oauthFlowTTL),
	})
	return OAuthStart{AuthorizationURL: authURL, Binding: binding}, nil
}

// CompleteOAuth finishes a sign-in from the authorization response: it
// checks the state, browser binding, and issuer, exchanges the code with the
// PKCE verifier, stores the tokens sealed, and reconnects the server. It
// returns the server name whenever the state matched a pending sign-in.
func (s *Service) CompleteOAuth(ctx context.Context, callback OAuthCallback) (string, error) {
	if s.oauth == nil || s.oauth.store == nil {
		return "", ErrOAuthUnavailable
	}
	flow, ok := s.oauth.takeFlow(callback.State)
	if !ok {
		return "", ErrOAuthFlowUnknown
	}
	if err := s.completeOAuth(ctx, flow, callback); err != nil {
		s.oauth.fail(flow.server, err)
		return flow.server, err
	}
	s.oauth.clearFailure(flow.server)
	// The sign-in is stored; other gateway instances reload it.
	defer s.notifyChanged(ctx)
	if err := s.Reload(ctx); err != nil {
		return flow.server, fmt.Errorf("signed in, but the server was not reconnected: %w", err)
	}
	return flow.server, nil
}

func (s *Service) completeOAuth(ctx context.Context, flow *oauthFlow, callback OAuthCallback) error {
	if flow.binding == "" || subtle.ConstantTimeCompare([]byte(flow.binding), []byte(callback.Binding)) != 1 {
		return errors.New("the sign-in was finished in a different browser than the one that started it")
	}
	if err := validateAuthorizationIssuer(callback.Issuer, flow.issuer, flow.issSupported); err != nil {
		return err
	}
	if callback.Error != "" {
		if callback.ErrorDescription != "" {
			return fmt.Errorf("the authorization server refused the sign-in: %s: %s", truncateOAuthText(callback.Error), truncateOAuthText(callback.ErrorDescription))
		}
		return fmt.Errorf("the authorization server refused the sign-in: %s", truncateOAuthText(callback.Error))
	}
	if callback.Code == "" {
		return errors.New("the authorization response carried no code")
	}
	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, s.oauth.httpClient)
	token, err := flow.config.Exchange(exchangeCtx, callback.Code,
		oauth2.VerifierOption(flow.verifier),
		oauth2.SetAuthURLParam("resource", flow.record.Resource),
	)
	if err != nil {
		return fmt.Errorf("exchange the authorization code: %w", oauthTokenError(err))
	}
	record := flow.record
	record.AccessToken = token.AccessToken
	record.RefreshToken = token.RefreshToken
	record.TokenType = token.TokenType
	record.Expiry = token.Expiry
	if granted, ok := token.Extra("scope").(string); ok && strings.TrimSpace(granted) != "" {
		record.Scopes = strings.Fields(granted)
	}
	now := time.Now().UTC()
	record.SignedInAt = now
	record.UpdatedAt = now
	if err := s.oauth.store.put(ctx, flow.server, record); err != nil {
		if !errors.Is(err, encryption.ErrSealUnconfirmed) {
			return err
		}
		// The record was written; only its data key is unconfirmed.
		slog.Warn("mcp oauth sign-in saved, but its data key could not be confirmed", "server", flow.server, "error", err)
	}
	return nil
}

// validateAuthorizationIssuer applies RFC 9207 as the MCP authorization spec
// requires: a present iss must equal the recorded issuer exactly, and an
// absent one is rejected only when the server advertises iss support.
func validateAuthorizationIssuer(iss, expected string, supported bool) error {
	if iss == "" {
		if supported {
			return errors.New("the authorization response lacks the iss parameter its server advertises")
		}
		return nil
	}
	if iss != expected {
		return errors.New("the authorization response came from an unexpected issuer")
	}
	return nil
}

// SignOutOAuth deletes the stored OAuth record of name and reconnects the
// server without it.
func (s *Service) SignOutOAuth(ctx context.Context, name string) error {
	if s.oauth == nil || s.oauth.store == nil {
		return ErrOAuthUnavailable
	}
	if _, ok := s.manager.spec(name); !ok {
		return ErrNotFound
	}
	if err := s.oauth.store.delete(ctx, name); err != nil {
		return err
	}
	s.oauth.clearFailure(name)
	defer s.notifyChanged(ctx)
	return s.Reload(ctx)
}

// forgetOAuth deletes a removed server's OAuth record. It is best effort:
// the server is already gone.
func (s *Service) forgetOAuth(ctx context.Context, name string) {
	if s.oauth == nil || s.oauth.store == nil {
		return
	}
	if err := s.oauth.store.delete(ctx, name); err != nil {
		slog.Warn("failed to delete the oauth record of a removed mcp server", "server", name, "error", err)
	}
	s.oauth.clearFailure(name)
}

// ValidateOAuthRedirectURI accepts an absolute http(s) URL without query or
// fragment whose path ends in OAuthCallbackPath, optionally under a base
// path.
func ValidateOAuthRedirectURI(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: redirect_uri must be an absolute http or https URL", ErrOAuthInvalidRequest)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("%w: redirect_uri must not carry a query, fragment, or credentials", ErrOAuthInvalidRequest)
	}
	if !strings.HasSuffix(parsed.Path, OAuthCallbackPath) {
		return fmt.Errorf("%w: redirect_uri must end in %s", ErrOAuthInvalidRequest, OAuthCallbackPath)
	}
	return nil
}

// attachOAuth gives specs the token sources of their stored sign-ins. A
// record that cannot be read keeps the server's current tokens, so a store
// hiccup never signs a working server out.
func (s *Service) attachOAuth(ctx context.Context, specs []ServerSpec) {
	if s.oauth == nil || s.oauth.store == nil {
		return
	}
	records, err := s.oauth.store.list(ctx)
	if err != nil {
		slog.Warn("some mcp oauth sign-ins could not be read; keeping their current tokens", "error", err)
	}
	s.oauth.attach(specs, records, err != nil)
}
