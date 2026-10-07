package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/encryption"
)

// ErrCredentialNotFound indicates a requested admin-managed provider
// credential row was not found.
var ErrCredentialNotFound = errors.New("provider credential not found")

// ManagedProviderCredential is one admin-managed provider instance: the
// dashboard equivalent of a `providers.<name>:` entry in config.yaml. Name is
// the configured provider instance name (what shows up as the qualifier in
// "name/model" selectors); Type selects the registered adapter (openai,
// anthropic, ollama, vertex, ...).
type ManagedProviderCredential struct {
	Name string
	Type string

	// APIKeys is the ordered credential rotation set. Keyless providers
	// (Ollama, vLLM) and Vertex/Bedrock (which authenticate a different way)
	// may leave this empty.
	APIKeys []string
	// SessionStickyKeys is nil for legacy/default rows and therefore means
	// enabled. A non-nil false value is the explicit dashboard/YAML opt-out.
	SessionStickyKeys *bool

	BaseURL                  string
	APIVersion               string
	Backend                  string
	AuthType                 string
	APIMode                  string
	VertexProject            string
	VertexLocation           string
	ServiceAccountFile       string
	ServiceAccountJSON       string
	ServiceAccountJSONBase64 string
	GCPScope                 string
	// ProxyURL is this provider's outbound HTTP(S)/SOCKS5 proxy; empty means
	// the gateway-wide default. It may carry credentials, so the admin API
	// masks it on the way out.
	ProxyURL string
	Models   []string

	// Enabled controls whether this credential is applied to the running
	// registry. Disabling one keeps the row (and its keys) on file without
	// routing traffic to it — the same effect as deleting it, without losing
	// the configuration.
	Enabled bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// toRawProviderConfig converts the admin row into the same shape config.yaml
// providers resolve from, so it can run through the existing credential
// resolution pipeline (normalizeProviderAPIKeys, filterEmptyProviders,
// buildProviderConfig) unmodified.
func (m ManagedProviderCredential) toRawProviderConfig() config.RawProviderConfig {
	raw := config.RawProviderConfig{
		Type:                     m.Type,
		APIKeys:                  m.APIKeys,
		SessionStickyKeys:        m.SessionStickyKeys,
		BaseURL:                  m.BaseURL,
		APIVersion:               m.APIVersion,
		Backend:                  m.Backend,
		AuthType:                 m.AuthType,
		APIMode:                  m.APIMode,
		VertexProject:            m.VertexProject,
		VertexLocation:           m.VertexLocation,
		ServiceAccountFile:       m.ServiceAccountFile,
		ServiceAccountJSON:       m.ServiceAccountJSON,
		ServiceAccountJSONBase64: m.ServiceAccountJSONBase64,
		GCPScope:                 m.GCPScope,
		ProxyURL:                 m.ProxyURL,
		Models:                   rawProviderModelsFromIDs(m.Models),
	}
	if len(m.APIKeys) > 0 {
		raw.APIKey = m.APIKeys[0]
	}
	return raw
}

// CredentialStore persists admin-managed provider credentials.
type CredentialStore interface {
	List(ctx context.Context) ([]ManagedProviderCredential, error)
	Get(ctx context.Context, name string) (*ManagedProviderCredential, error)
	Upsert(ctx context.Context, cred ManagedProviderCredential) error
	Delete(ctx context.Context, name string) error
	Close() error
}

// CredentialsService merges declarative (config.yaml / env var) provider
// names with the admin-managed credential store and reconciles the result
// into the live factory/registry — the provider-credentials equivalent of
// mcpgateway.Service. Declarative names are read-only here (shadowed),
// mirroring every other config-store precedence rule in the gateway.
type CredentialsService struct {
	factory  *ProviderFactory
	registry *ModelRegistry
	store    CredentialStore

	managedNames map[string]struct{}
	resilience   config.ResilienceConfig
	// secrets resolves the secret references stored credentials hold, with
	// the schemes of the generation this service belongs to.
	secrets *config.Secrets

	// applyMu serializes saves, deletes, and secret rotation, each of which
	// builds from the stored row and then installs the result.
	applyMu sync.Mutex

	// configs holds the effective ProviderConfig of every credential that is
	// currently installed in the registry, so the admin status endpoint can
	// report the same merged resilience settings for dashboard-registered
	// providers that it reports for config.yaml/env ones.
	mu      sync.RWMutex
	configs map[string]ProviderConfig
	// keyrings holds each installed provider's keyring, so a rotated API key
	// is swapped in place rather than rebuilding the provider.
	keyrings map[string]*Keyring
}

// NewCredentialsService builds the service and applies every currently
// stored, enabled, non-shadowed credential to the registry before returning.
// secrets resolves the secret references credentials hold; nil resolves the
// built-in env and file schemes only.
func NewCredentialsService(ctx context.Context, factory *ProviderFactory, registry *ModelRegistry, store CredentialStore, declaredNames []string, resilience config.ResilienceConfig, secrets *config.Secrets) (*CredentialsService, error) {
	if factory == nil {
		return nil, fmt.Errorf("provider factory is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("model registry is required")
	}
	if store == nil {
		return nil, fmt.Errorf("credential store is required")
	}

	managed := make(map[string]struct{}, len(declaredNames))
	for _, name := range declaredNames {
		name = strings.TrimSpace(name)
		if name != "" {
			managed[name] = struct{}{}
		}
	}

	s := &CredentialsService{
		factory:      factory,
		registry:     registry,
		store:        store,
		managedNames: managed,
		resilience:   resilience,
		secrets:      secrets,
		configs:      make(map[string]ProviderConfig),
		keyrings:     make(map[string]*Keyring),
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// RegisteredTypes lists every provider type the factory can construct, for
// the admin create-form's type picker.
func (s *CredentialsService) RegisteredTypes() []string {
	return s.factory.RegisteredTypes()
}

// CredentialSchemas returns the credential form of every registered provider
// type, so the admin create/edit form can offer exactly the fields the
// selected type uses.
func (s *CredentialsService) CredentialSchemas() []CredentialSchema {
	return s.factory.CredentialSchemas()
}

// CredentialSchema returns one provider type's credential form.
func (s *CredentialsService) CredentialSchema(providerType string) CredentialSchema {
	return credentialSchema(providerType, s.factory.discoveryConfig(providerType))
}

// IsManaged reports whether name is declared in config.yaml/env, making it
// read-only from the admin API's point of view.
func (s *CredentialsService) IsManaged(name string) bool {
	_, ok := s.managedNames[strings.TrimSpace(name)]
	return ok
}

// Reload re-registers every enabled, non-shadowed stored credential into the
// registry (pure in-memory work) and then kicks a non-blocking model-inventory
// fetch so the newly registered providers' models become routable without
// delaying the caller — the same async startup contract providers.Init uses
// for declarative providers, so adding the credentials store never turns
// gateway startup into a synchronous network round trip to every provider.
// Declarative providers are untouched: providers.Init already registered them.
func (s *CredentialsService) Reload(ctx context.Context) error {
	rows, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list provider credentials: %w", err)
	}

	registeredAny := false
	for _, row := range rows {
		if s.IsManaged(row.Name) {
			slog.Warn("provider credential from admin store is shadowed by config/env", "provider", row.Name)
			continue
		}
		if !row.Enabled {
			continue
		}
		if err := s.register(ctx, row); err != nil {
			slog.Error("failed to apply stored provider credential", "provider", row.Name, "error", err)
			continue
		}
		registeredAny = true
	}
	if registeredAny {
		s.registry.InitializeAsync(ctx)
	}
	return nil
}

// List returns every admin-managed credential row (secrets included; callers
// at the HTTP boundary are responsible for redaction).
func (s *CredentialsService) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	return s.store.List(ctx)
}

// Get returns one admin-managed credential row.
func (s *CredentialsService) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	return s.store.Get(ctx, name)
}

// Upsert validates, persists, and hot-applies one admin-managed provider
// credential. A disabled row is persisted but unregistered from the live
// registry, the same end state as deleting it.
func (s *CredentialsService) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	name := strings.TrimSpace(cred.Name)
	if name == "" {
		return &CredentialFieldError{Field: "name", Message: "provider name is required"}
	}
	if s.IsManaged(name) {
		return fmt.Errorf("provider %q is managed by config/env and is read-only", name)
	}
	if strings.TrimSpace(cred.Type) == "" {
		return &CredentialFieldError{Field: "type", Message: "provider type is required"}
	}
	// "/" is the model-selector qualifier delimiter ("name/model"); a name
	// containing one would make the provider unreachable or ambiguous
	// through that syntax.
	if strings.Contains(name, "/") {
		return &CredentialFieldError{Field: "name", Message: fmt.Sprintf("provider name %q must not contain '/'", name)}
	}
	if !s.factory.knowsType(cred.Type) {
		return &CredentialFieldError{Field: "type", Message: "unknown provider type: " + cred.Type}
	}
	cred.Name = name
	// ErrSealUnconfirmed means the row was written and applied: refresh like
	// any other save, then report the error.
	saveErr := s.apply(ctx, cred)
	if saveErr != nil && !errors.Is(saveErr, encryption.ErrSealUnconfirmed) {
		return saveErr
	}
	// A Refresh error here means the provider's /models call itself failed
	// (bad key, unreachable host, ...) -- registration still succeeded, and
	// the provider correctly shows Unhealthy on the status endpoint instead
	// of silently vanishing. That is not a save failure: an admin fixing a
	// typo'd API key should see "saved", not a scary error on every attempt
	// until the key happens to be valid.
	if err := s.registry.Refresh(ctx); err != nil {
		slog.Warn("provider credential saved but its model catalog refresh failed", "provider", name, "error", err)
	}
	return saveErr
}

// Delete removes one admin-managed provider credential and unregisters it
// from the live registry.
func (s *CredentialsService) Delete(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if s.IsManaged(name) {
		return fmt.Errorf("provider %q is managed by config/env and is read-only", name)
	}
	if err := s.deleteStored(ctx, name); err != nil {
		return err
	}
	// See the matching comment in Upsert: a Refresh failure here reflects a
	// remaining provider's own health, not whether the delete succeeded.
	if err := s.registry.Refresh(ctx); err != nil {
		slog.Warn("provider credential deleted but the model catalog refresh failed", "provider", name, "error", err)
	}
	return nil
}

// deleteStored deletes the stored row name, unregisters it, and releases the
// secrets it held.
func (s *CredentialsService) deleteStored(ctx context.Context, name string) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	previous, err := s.store.Get(ctx, name)
	if err != nil && !errors.Is(err, ErrCredentialNotFound) {
		return err
	}
	if err := s.store.Delete(ctx, name); err != nil {
		return err
	}
	s.remove(name)
	s.releaseSecrets(ctx, name, credentialSecretValues(previous), nil)
	return nil
}

// apply validates, persists, and installs one credential row: the part of
// Upsert that touches the store and the registry.
//
// Literal secrets are written through the generation's SecretWriter first,
// when one is registered, so the row is stored with references only. The row
// is then resolved and its adapter constructed before anything is persisted
// or the registry touched: an unresolvable row is not worth storing, and if
// this is an edit, whatever is currently registered under this name --
// possibly a working provider actively serving traffic -- must keep serving
// rather than being unregistered out from under a failed edit.
func (s *CredentialsService) apply(ctx context.Context, cred ManagedProviderCredential) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	previous, err := s.store.Get(ctx, cred.Name)
	if err != nil && !errors.Is(err, ErrCredentialNotFound) {
		return err
	}
	kept := credentialSecretValues(previous)
	written, err := s.storeCredentialSecrets(ctx, &cred, kept)
	if err != nil {
		return err
	}
	built, err := s.buildCredential(ctx, cred)
	if err != nil {
		// Nothing was stored, so whatever the stored row holds stays.
		s.releaseSecrets(ctx, cred.Name, written, kept)
		return err
	}
	// ErrSealUnconfirmed means the row was written: apply it like any other
	// save, then report the error.
	saveErr := s.store.Upsert(ctx, cred)
	if saveErr != nil && !errors.Is(saveErr, encryption.ErrSealUnconfirmed) {
		// The stored row is unchanged, so whatever it holds stays.
		s.releaseSecrets(ctx, cred.Name, written, kept)
		return saveErr
	}

	if cred.Enabled {
		s.install(cred.Name, built)
	} else {
		s.remove(cred.Name)
	}
	s.releaseSecrets(ctx, cred.Name, kept, credentialSecretValues(&cred))
	return saveErr
}

// builtCredential is a credential row resolved and constructed, ready to
// install. provider is nil for a disabled row.
type builtCredential struct {
	provider core.Provider
	cfg      ProviderConfig
	keys     *Keyring
	secrets  *config.ResolvedEntity
}

// buildCredential resolves the row's secret references, validates the result
// against the type's credential form, and constructs the adapter when the row
// is enabled. A disabled row is still resolved, so a reference that cannot be
// resolved is rejected when it is saved rather than when it is enabled.
func (s *CredentialsService) buildCredential(ctx context.Context, cred ManagedProviderCredential) (builtCredential, error) {
	resolved, secrets, err := s.resolveCredential(ctx, cred)
	if err != nil {
		return builtCredential{}, err
	}
	schema := s.CredentialSchema(cred.Type)
	if err := validateCredential(resolved, schema); err != nil {
		return builtCredential{}, err
	}
	if !cred.Enabled {
		return builtCredential{}, nil
	}
	built, err := s.buildProvider(resolved)
	if err != nil {
		return builtCredential{}, unappliableCredentialError(cred, schema, err)
	}
	built.secrets = secrets
	return built, nil
}

// register resolves one stored credential row and registers the resulting
// adapter into the registry. It does not refresh the model inventory; callers
// batch that after registering. Only used by Reload (initial population),
// where there is no previously-live provider to protect, so build-then-install
// in one step is safe.
func (s *CredentialsService) register(ctx context.Context, row ManagedProviderCredential) error {
	row.Name = strings.TrimSpace(row.Name)
	resolved, secrets, err := s.resolveCredential(ctx, row)
	if err != nil {
		return err
	}
	built, err := s.buildProvider(resolved)
	if err != nil {
		return err
	}
	built.secrets = secrets
	s.install(row.Name, built)
	return nil
}

// buildProvider runs one resolved credential row through the same pipeline
// declarative providers use (env-placeholder rejection, API key
// de-duplication, resilience merge) and constructs the adapter, without
// touching the registry. Callers that already have something live registered
// under this name must build+validate first and only call install on
// success, so a bad edit never displaces a working provider.
func (s *CredentialsService) buildProvider(row ManagedProviderCredential) (builtCredential, error) {
	cfg, err := s.providerConfig(row)
	if err != nil {
		return builtCredential{}, err
	}
	keys := NewKeyringWithSessionStickiness(cfg.SessionStickyKeys, cfg.APIKeys...)
	provider, err := s.factory.create(cfg, keys)
	if err != nil {
		return builtCredential{}, err
	}
	return builtCredential{provider: provider, cfg: cfg, keys: keys}, nil
}

// providerConfig resolves one row, secret references already resolved, into
// the effective configuration of its provider.
func (s *CredentialsService) providerConfig(row ManagedProviderCredential) (ProviderConfig, error) {
	name := strings.TrimSpace(row.Name)
	raw := map[string]config.RawProviderConfig{name: row.toRawProviderConfig()}
	resolved := filterEmptyProviders(normalizeProviderAPIKeys(raw), s.factory.discoveryConfigsSnapshot())
	rawCfg, ok := resolved[name]
	if !ok {
		return ProviderConfig{}, fmt.Errorf("credentials did not resolve (missing API key or required fields)")
	}
	cfg := buildProviderConfig(rawCfg, s.resilience)
	cfg.Name = name
	return cfg, nil
}

// install unregisters whatever is currently registered under name (a no-op
// if nothing is), registers the built provider in its place, records its
// effective configuration, and records its secret references for rotation.
func (s *CredentialsService) install(name string, built builtCredential) {
	cfg := built.cfg
	s.registry.UnregisterProvider(name)
	s.registry.RegisterProviderWithNameAndType(built.provider, name, cfg.Type)
	if len(cfg.Models) > 0 {
		s.registry.SetProviderConfiguredModels(name, cfg.Models)
	}
	if len(cfg.ModelMetadataOverrides) > 0 {
		s.registry.SetProviderMetadataOverrides(name, cfg.ModelMetadataOverrides)
	}
	s.registry.SetProviderModelFilter(name, cfg.ModelFilter)

	s.mu.Lock()
	s.configs[name] = cfg
	s.keyrings[name] = built.keys
	s.mu.Unlock()
	built.secrets.Record()
}

// remove unregisters name from the registry and forgets its effective
// configuration and secret references.
func (s *CredentialsService) remove(name string) {
	s.registry.UnregisterProvider(name)

	s.mu.Lock()
	delete(s.configs, name)
	delete(s.keyrings, name)
	s.mu.Unlock()
	s.secrets.ForgetEntity(credentialSecretEntity(name))
}

// ConfiguredProviders returns the admin-safe effective configuration of every
// credential currently installed in the registry, in the same shape
// providers.Init produces for config.yaml/env providers.
func (s *CredentialsService) ConfiguredProviders() []SanitizedProviderConfig {
	s.mu.RLock()
	configs := make(map[string]ProviderConfig, len(s.configs))
	maps.Copy(configs, s.configs)
	s.mu.RUnlock()
	return SanitizeProviderConfigs(configs)
}
