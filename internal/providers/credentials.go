package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
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
	Models                   []string

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
}

// NewCredentialsService builds the service and applies every currently
// stored, enabled, non-shadowed credential to the registry before returning.
func NewCredentialsService(ctx context.Context, factory *ProviderFactory, registry *ModelRegistry, store CredentialStore, declaredNames []string, resilience config.ResilienceConfig) (*CredentialsService, error) {
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
		if err := s.register(row); err != nil {
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
	if err := validateCredential(cred, s.CredentialSchema(cred.Type)); err != nil {
		return err
	}

	// Resolve and construct the adapter before persisting or touching the
	// registry: an unresolvable row is not worth storing, and if this is an
	// edit, whatever is currently registered under this name -- possibly a
	// working provider actively serving traffic -- must keep serving rather
	// than being unregistered out from under a failed edit.
	var provider core.Provider
	var cfg ProviderConfig
	if cred.Enabled {
		var err error
		provider, cfg, err = s.buildProvider(cred)
		if err != nil {
			return unappliableCredentialError(cred, s.CredentialSchema(cred.Type), err)
		}
	}

	if err := s.store.Upsert(ctx, cred); err != nil {
		return err
	}

	if cred.Enabled {
		s.install(name, provider, cfg)
	} else {
		s.registry.UnregisterProvider(name)
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
	return nil
}

// Delete removes one admin-managed provider credential and unregisters it
// from the live registry.
func (s *CredentialsService) Delete(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if s.IsManaged(name) {
		return fmt.Errorf("provider %q is managed by config/env and is read-only", name)
	}
	if err := s.store.Delete(ctx, name); err != nil {
		return err
	}
	s.registry.UnregisterProvider(name)
	// See the matching comment in Upsert: a Refresh failure here reflects a
	// remaining provider's own health, not whether the delete succeeded.
	if err := s.registry.Refresh(ctx); err != nil {
		slog.Warn("provider credential deleted but the model catalog refresh failed", "provider", name, "error", err)
	}
	return nil
}

// register resolves one credential row through the same pipeline declarative
// providers use (env-placeholder rejection, API key de-duplication, resilience
// merge) and registers the resulting adapter into the registry. It does not
// refresh the model inventory; callers batch that after registering. Only
// used by Reload (initial population), where there is no previously-live
// provider to protect, so build-then-install in one step is safe.
func (s *CredentialsService) register(row ManagedProviderCredential) error {
	provider, cfg, err := s.buildProvider(row)
	if err != nil {
		return err
	}
	s.install(strings.TrimSpace(row.Name), provider, cfg)
	return nil
}

// buildProvider resolves one credential row through the same pipeline
// declarative providers use (env-placeholder rejection, API key
// de-duplication, resilience merge) and constructs the adapter, without
// touching the registry. Callers that already have something live registered
// under this name must build+validate first and only call install on
// success, so a bad edit never displaces a working provider.
func (s *CredentialsService) buildProvider(row ManagedProviderCredential) (core.Provider, ProviderConfig, error) {
	name := strings.TrimSpace(row.Name)
	raw := map[string]config.RawProviderConfig{name: row.toRawProviderConfig()}
	resolved := filterEmptyProviders(normalizeProviderAPIKeys(raw), s.factory.discoveryConfigsSnapshot())
	rawCfg, ok := resolved[name]
	if !ok {
		return nil, ProviderConfig{}, fmt.Errorf("credentials did not resolve (missing API key or required fields)")
	}

	cfg := buildProviderConfig(rawCfg, s.resilience)
	provider, err := s.factory.Create(cfg)
	if err != nil {
		return nil, ProviderConfig{}, err
	}
	return provider, cfg, nil
}

// install unregisters whatever is currently registered under name (a no-op
// if nothing is) and registers provider in its place.
func (s *CredentialsService) install(name string, provider core.Provider, cfg ProviderConfig) {
	s.registry.UnregisterProvider(name)
	s.registry.RegisterProviderWithNameAndType(provider, name, cfg.Type)
	if len(cfg.Models) > 0 {
		s.registry.SetProviderConfiguredModels(name, cfg.Models)
	}
	if len(cfg.ModelMetadataOverrides) > 0 {
		s.registry.SetProviderMetadataOverrides(name, cfg.ModelMetadataOverrides)
	}
}
